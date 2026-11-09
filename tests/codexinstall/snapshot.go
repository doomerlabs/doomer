package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const fingerprintSchema = 1

type snapshotEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}
type externalInput struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}
type snapshotMetadata struct {
	Schema        int               `json:"schema"`
	Files         []snapshotEntry   `json:"files"`
	Settings      map[string]string `json:"settings"`
	External      []externalInput   `json:"external"`
	Environment   string            `json:"environment"`
	OS            string            `json:"os"`
	Arch          string            `json:"arch"`
	Commit        string            `json:"commit,omitempty"`
	Baseline      string            `json:"baseline,omitempty"`
	Stable        json.RawMessage   `json:"stable,omitempty"`
	Captured      time.Time         `json:"captured"`
	CacheDisabled string            `json:"cache_disabled,omitempty"`
}

type snapshotOptions struct {
	Output, Agent, Candidate, Bootstrap, Skills, Guide string
	Settings                                           map[string]string
	Local                                              bool
}

func byteHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Credentials, execution output and build products never enter source snapshots.
// Use Git's tracked + untracked (non-ignored) inventory so added/deleted source
// files participate even before a commit, without copying private Git metadata.
func excludedSnapshotPath(path string) bool {
	path = filepath.ToSlash(path)
	// Root build outputs are irrelevant; nested tracked dist files can be embedded
	// template inputs and must remain in the captured tree.
	for _, root := range []string{"dist", ".release-dist", "bin", "reports"} {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	for _, part := range strings.Split(path, "/") {
		lower := strings.ToLower(part)
		if lower == ".git" || lower == ".direnv" || lower == "node_modules" || lower == ".cache" || lower == "__pycache__" || strings.HasSuffix(lower, ".pyc") || lower == "auth.json" || lower == "credentials" || lower == "credentials.json" || lower == ".aws" || strings.HasPrefix(lower, ".env") || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") || strings.HasSuffix(lower, ".test") || strings.HasSuffix(lower, "-runs") || strings.HasPrefix(lower, "codex-install-report") {
			return true
		}
	}
	return false
}

// Exclude only explicitly selected nested sources from their parent's inventory.
// They are captured under their own role, including all of their source bytes.
func nestedSnapshotSources(root string, sources map[string]string) []string {
	var nested []string
	seen := map[string]bool{}
	for _, source := range sources {
		relative, err := filepath.Rel(root, source)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || seen[source] {
			continue
		}
		seen[source] = true
		nested = append(nested, source)
	}
	sort.Strings(nested)
	return nested
}

func sourceInventory(ctx context.Context, root, prefix, output string, excludedRoots ...string) ([]snapshotEntry, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("inventory source: %w", err)
	}
	seen := map[string]bool{}
	var entries []snapshotEntry
	for _, p := range strings.Split(string(raw), "\x00") {
		if p == "" || seen[p] || excludedSnapshotPath(p) {
			continue
		}
		seen[p] = true
		full := filepath.Join(root, p)
		excluded := false
		for _, excludedRoot := range excludedRoots {
			relative, err := filepath.Rel(excludedRoot, full)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		absolute, _ := filepath.Abs(full)
		outputDir := filepath.Dir(output)
		outputRelative, outputRelativeErr := filepath.Rel(root, outputDir)
		if output != "" && outputRelativeErr == nil && outputRelative != "." && outputRelative != ".." && !strings.HasPrefix(outputRelative, ".."+string(filepath.Separator)) {
			relative, e := filepath.Rel(outputDir, absolute)
			if e == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
		}
		if absolute == output || absolute == output+".inputs.json" || absolute == output+".tmp" {
			continue
		}
		info, err := os.Lstat(full)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("snapshot requires regular source files: %s", p)
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		mode := uint32(0600)
		if info.Mode()&0111 != 0 {
			mode = 0700
		}
		entries = append(entries, snapshotEntry{filepath.ToSlash(filepath.Join(prefix, p)), byteHash(raw), mode})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func copySource(ctx context.Context, root, destination, prefix, output string, excludedRoots ...string) ([]snapshotEntry, error) {
	before, err := sourceInventory(ctx, root, prefix, output, excludedRoots...)
	if err != nil {
		return nil, err
	}
	for _, entry := range before {
		relative := strings.TrimPrefix(entry.Path, prefix+"/")
		raw, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			return nil, err
		}
		if byteHash(raw) != entry.SHA256 {
			return nil, fmt.Errorf("source changed during capture: %s", relative)
		}
		target := filepath.Join(destination, entry.Path)
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		if err = os.WriteFile(target, raw, os.FileMode(entry.Mode&^0200)); err != nil {
			return nil, err
		}
	}
	after, err := sourceInventory(ctx, root, prefix, output, excludedRoots...)
	if err != nil {
		return nil, err
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		return nil, fmt.Errorf("source changed during capture; rerun once edits stabilize")
	}
	return before, nil
}

func (m snapshotMetadata) fingerprint(scenario string) string {
	// Provenance and temporary absolute paths are deliberately outside identity.
	raw, _ := json.Marshal(struct {
		Schema                                    int               `json:"schema"`
		Files                                     []snapshotEntry   `json:"files"`
		Settings                                  map[string]string `json:"settings"`
		External                                  []externalInput   `json:"external"`
		Environment, OS, Arch, Baseline, Scenario string
	}{m.Schema, m.Files, m.Settings, m.External, m.Environment, m.OS, m.Arch, m.Baseline, scenario})
	return byteHash(raw)
}

func runSnapshot(ctx context.Context, opts snapshotOptions, args []string) error {
	if opts.Settings["GOFLAGS"] != "" {
		return fmt.Errorf("captured runs require an empty GOFLAGS so external overlays cannot bypass the snapshot")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return err
	}
	for _, p := range []string{output, output + ".inputs.json"} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("existing report %s; choose a new output prefix", p)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	dir, err := os.MkdirTemp("", "doomer-e2e-snapshot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	metadata := snapshotMetadata{Schema: fingerprintSchema, Settings: opts.Settings, OS: runtime.GOOS, Arch: runtime.GOARCH, Environment: "local", Captured: time.Now().UTC()}
	if os.Getenv("CI") != "" {
		metadata.Environment = "ci"
	}
	commit, _ := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
	metadata.Commit = strings.TrimSpace(string(commit))
	sources := map[string]string{"cli": root}
	for role, source := range map[string]string{"candidate": opts.Candidate, "bootstrap": opts.Bootstrap, "skills": opts.Skills} {
		if source != "" {
			abs, err := filepath.Abs(source)
			if err != nil {
				return err
			}
			sources[role] = abs
		}
	}
	// Deterministic roles; identical paths can be copied once and reused.
	roles := []string{"cli", "candidate", "bootstrap", "skills"}
	captured := map[string]string{}
	mapped := map[string]string{}
	for _, role := range roles {
		source, ok := sources[role]
		if !ok {
			continue
		}
		if previous, ok := captured[source]; ok {
			mapped[role] = previous
			continue
		}
		entries, err := copySource(ctx, source, dir, role, output, nestedSnapshotSources(source, sources)...)
		if err != nil {
			return err
		}
		metadata.Files = append(metadata.Files, entries...)
		mapped[role] = filepath.Join(dir, role)
		captured[source] = mapped[role]
	}
	sort.Slice(metadata.Files, func(i, j int) bool { return metadata.Files[i].Path < metadata.Files[j].Path })
	toolchain, err := exec.CommandContext(ctx, "go", "version").Output()
	if err != nil {
		return err
	}
	metadata.Settings["go_toolchain"] = strings.TrimSpace(string(toolchain))
	buildEnv, err := exec.CommandContext(ctx, "go", "env", "-json", "GOFLAGS", "CGO_ENABLED", "GOOS", "GOARCH", "GOTOOLCHAIN", "GOEXPERIMENT").Output()
	if err != nil {
		return err
	}
	var effective map[string]string
	if err = json.Unmarshal(buildEnv, &effective); err != nil {
		return err
	}
	if effective["GOFLAGS"] != "" {
		return fmt.Errorf("captured runs require an empty effective GOFLAGS")
	}
	for key, value := range effective {
		metadata.Settings["go_env_"+key] = value
	}
	metadata.Settings["go_env_GOWORK"] = "off"
	if opts.Agent != "" {
		executable, entries, err := captureAgentPackage(ctx, opts.Agent, dir)
		if err != nil {
			return err
		}
		opts.Agent = executable
		metadata.Files = append(metadata.Files, entries...)
	} else {
		metadata.CacheDisabled = "shared reuse currently requires a pinned local agent package"
	}

	guide := opts.Guide
	if opts.Local {
		frozenOptions := opts
		frozenOptions.Skills = mapped["skills"]
		frozen, err := freezeExternalInputs(ctx, dir, frozenOptions, &metadata)
		if err != nil {
			metadata.Stable = nil
			metadata.CacheDisabled = err.Error()
			fmt.Fprintln(os.Stderr, "Cache disabled:", err)
		} else {
			guide = frozen
		}
	}
	sort.Slice(metadata.Files, func(i, j int) bool { return metadata.Files[i].Path < metadata.Files[j].Path })
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	metaPath := filepath.Join(dir, "inputs.json")
	if err = os.WriteFile(metaPath, append(raw, '\n'), 0600); err != nil {
		return err
	}
	if err = os.WriteFile(output+".inputs.json", append(raw, '\n'), 0600); err != nil {
		return err
	}
	runner := filepath.Join(dir, "runner")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-buildvcs=false", "-o", runner, "./tests/codexinstall")
	build.Dir = mapped["cli"]
	build.Env = append(os.Environ(), "GOWORK=off")
	build.Stderr = os.Stderr
	if err = build.Run(); err != nil {
		return fmt.Errorf("build captured runner: %w", err)
	}
	overrides := []string{"--snapshot-input", metaPath, "--output", output}
	for role, flag := range map[string]string{"candidate": "--candidate-source", "bootstrap": "--bootstrap-source", "skills": "--skills-source"} {
		if p := mapped[role]; p != "" {
			overrides = append(overrides, flag, p)
		}
	}
	if opts.Candidate == "" && metadata.Settings["candidate-release"] != "" && opts.Local {
		overrides = append(overrides, "--candidate-source", mapped["cli"])
	}
	if opts.Agent != "" {
		agentFlag := "--local-codex"
		if metadata.Settings["harness"] == "claude" {
			agentFlag = "--local-claude"
		}
		overrides = append(overrides, agentFlag, opts.Agent)
	}
	if guide != "" {
		overrides = append(overrides, "--install-guide", guide)
	}
	// All test reads/builds now resolve inside the captured source tree.
	child := exec.CommandContext(ctx, runner, append(append([]string(nil), args...), overrides...)...)
	child.Dir = mapped["cli"]
	child.Env = append(os.Environ(), "GOWORK=off")
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Cancel = func() error { return child.Process.Signal(os.Interrupt) }
	child.WaitDelay = 75 * time.Second
	return child.Run()
}

// Execute the captured agent package too: updating the dedicated source package
// during a suite cannot change the executable or its helper resources mid-run.
func captureAgentPackage(ctx context.Context, agent, destination string) (string, []snapshotEntry, error) {
	agent, err := filepath.Abs(agent)
	if err != nil {
		return "", nil, err
	}
	packageRoot := agent
	if filepath.Base(filepath.Dir(agent)) == "bin" {
		packageRoot = filepath.Dir(filepath.Dir(agent))
	}
	var entries []snapshotEntry
	executableRelative, err := filepath.Rel(packageRoot, agent)
	if err != nil {
		return "", nil, err
	}
	if executableRelative == "." {
		executableRelative = filepath.Base(agent)
	}
	err = filepath.WalkDir(packageRoot, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular agent package input: %s", path)
		}
		relative, err := filepath.Rel(packageRoot, path)
		if err != nil {
			return err
		}
		if relative == "." {
			relative = filepath.Base(path)
		}
		target := filepath.Join(destination, "agent", relative)
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		defer source.Close()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0400|info.Mode().Perm()&0111)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(h, output), source)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		entries = append(entries, snapshotEntry{"agent/" + filepath.ToSlash(relative), hex.EncodeToString(h.Sum(nil)), uint32(info.Mode().Perm() & 0111)})
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(destination, "agent", executableRelative), entries, nil
}
