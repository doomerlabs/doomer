package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
	watching "github.com/doomerlabs/doomer/internal/watch"
)

func putRuntime(t *testing.T, home, release string) {
	t.Helper()
	path := runtimeversion.Binary(home, release)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("#!/bin/sh\nif [ \"$1\" = runtime-info ]; then printf '{\"protocol\":1}\\n'; else printf '" + release + " %s\\n' \"$DOOMER_SELECTED_PIN\"; fi\n")
	if err := os.WriteFile(path, body, 0755); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(body)
	rec := selfupdate.Record{SchemaVersion: 1, Manager: "doomer-review", Path: path, Version: release, SHA256: hex.EncodeToString(hash[:])}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "installation.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func putPin(t *testing.T, repo, release string) {
	t.Helper()
	path := filepath.Join(repo, ".agents/skills/doomer-review/references")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "install.md"), []byte("## Select CLI release "+release+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLauncherSelectsThreeRepositoryPins(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("versioned release execution supports macOS and Linux")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	original := launcherExecutable
	launcherExecutable = func() (string, error) { return filepath.Join(home, ".local/bin/doomer"), nil }
	t.Cleanup(func() { launcherExecutable = original })
	for _, release := range []string{"2026.11.3.7", "2026.11.4.1", "2026.11.5.1"} {
		repo := newWatchTestRepo(t)
		putPin(t, repo, release)
		putRuntime(t, home, release)
		var out bytes.Buffer
		handled, err := DispatchCLI(context.Background(), []string{"onboard", "--repo", repo}, strings.NewReader(""), &out, &out)
		if !handled || err != nil || !strings.HasPrefix(out.String(), release+" ") || !strings.Contains(out.String(), repo) {
			t.Fatal(handled, err, out.String())
		}
		out.Reset()
		handled, err = DispatchCLI(context.Background(), []string{"watch", "status", repo}, nil, &out, &out)
		if !handled || err != nil || !strings.HasPrefix(out.String(), release+" ") {
			t.Fatal(handled, err, out.String())
		}
	}
}

func TestVersionedWatchStateSharesConsentAndStableHook(t *testing.T) {
	t.Setenv("DOOMER_CONFIG_DIR", t.TempDir())
	original := activeRuntimeVersion
	t.Cleanup(func() { activeRuntimeVersion = original })
	activeRuntimeVersion = func() string { return "2026.11.3.7" }
	a, consentA, err := watchPaths("https://doomer.ai/api", "default")
	if err != nil {
		t.Fatal(err)
	}
	commandA, err := watchHookCommand("https://doomer.ai/api", "default", a)
	if err != nil {
		t.Fatal(err)
	}
	activeRuntimeVersion = func() string { return "2026.11.4.1" }
	b, consentB, err := watchPaths("https://doomer.ai/api", "default")
	if err != nil {
		t.Fatal(err)
	}
	commandB, err := watchHookCommand("https://doomer.ai/api", "default", b)
	if err != nil {
		t.Fatal(err)
	}
	if a == b || consentA != consentB || commandA != commandB || !strings.Contains(commandA, "--dispatch") {
		t.Fatal(a, b, consentA, consentB, commandA, commandB)
	}
}

func TestRuntimeScopeFollowsCheckoutPin(t *testing.T) {
	repo := newWatchTestRepo(t)
	putPin(t, repo, "2026.11.3.7")
	cfg := watching.Config{Directories: []string{repo}, RuntimeVersion: "2026.11.3.7"}
	if !watching.Allowed(cfg, repo) {
		t.Fatal("matching runtime denied")
	}
	cfg.RuntimeVersion = "2026.11.4.1"
	if watching.Allowed(cfg, repo) {
		t.Fatal("wrong runtime accepted")
	}
	putPin(t, repo, "2026.11.4.1")
	if !watching.Allowed(cfg, repo) {
		t.Fatal("pin update did not select new runtime")
	}
}

func TestRuntimeMergePreservesAllReviewResults(t *testing.T) {
	got := mergeHookOutputs([]map[string]any{{"decision": "block", "reason": "first", "hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "additionalContext": "a"}}, {}, {"decision": "block", "reason": "second", "hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "additionalContext": "b"}}})
	if got["decision"] != "block" || got["reason"] != "first\n\nsecond" || got["hookSpecificOutput"].(map[string]any)["additionalContext"] != "a\n\nb" {
		t.Fatal(got)
	}
}

func TestLauncherRefusesMissingOrModifiedPinnedRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("versioned release execution supports macOS and Linux")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	original := launcherExecutable
	launcherExecutable = func() (string, error) { return filepath.Join(home, ".local/bin/doomer"), nil }
	t.Cleanup(func() { launcherExecutable = original })
	repo := newWatchTestRepo(t)
	release := "2026.11.3.7"
	putPin(t, repo, release)
	for _, modified := range []bool{false, true} {
		if modified {
			putRuntime(t, home, release)
			os.WriteFile(runtimeversion.Binary(home, release), []byte("changed"), 0755)
		}
		var out bytes.Buffer
		handled, err := DispatchCLI(context.Background(), []string{"onboard", "--repo", repo}, nil, &out, &out)
		if !handled || err == nil || out.Len() != 0 {
			t.Fatal("fell back from pinned release", handled, err, out.String())
		}
	}
}

func TestRuntimeDispatcherPreservesResultsFromThreeVersions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("versioned release execution supports macOS and Linux")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOOMER_CONFIG_DIR", t.TempDir())
	original := activeRuntimeVersion
	activeRuntimeVersion = func() string { return "" }
	t.Cleanup(func() { activeRuntimeVersion = original })
	api, profile := "https://doomer.ai/api", "default"
	root, _, err := consentPaths(api, profile)
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range []string{"2026.11.3.7", "2026.11.4.1", "2026.11.5.1"} {
		putRuntime(t, home, release)
		path := runtimeversion.Binary(home, release)
		body := []byte("#!/bin/sh\ncat >/dev/null\nprintf '{\"decision\":\"block\",\"reason\":\"" + release + "\"}\\n'\n")
		os.WriteFile(path, body, 0755)
		sum := sha256.Sum256(body)
		raw, _ := json.Marshal(selfupdate.Record{SchemaVersion: 1, Manager: "doomer-review", Path: path, Version: release, SHA256: hex.EncodeToString(sum[:])})
		os.WriteFile(filepath.Join(filepath.Dir(path), "installation.json"), raw, 0600)
		os.MkdirAll(filepath.Join(root, "runtimes", release), 0700)
	}
	var out bytes.Buffer
	c := newRuntimeInfoCommand()
	c.SetContext(context.Background())
	c.SetIn(strings.NewReader(`{"session_id":"session","hook_event_name":"Stop"}`))
	c.SetOut(&out)
	if err := dispatchRuntimeHooks(c, api, profile, root, "codex"); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["decision"] != "block" || result["reason"] != "2026.11.3.7\n\n2026.11.4.1\n\n2026.11.5.1" {
		t.Fatal(result)
	}
}

func TestVersionedRuntimeHonorsLegacyPause(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profile")
	runtime := filepath.Join(root, "runtimes", "2026.11.3.7")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "paused.json"), []byte("true"), 0600); err != nil {
		t.Fatal(err)
	}
	if !paused(runtime) {
		t.Fatal("ignored existing profile pause")
	}
}

func TestLauncherMigrationRequiresEveryConfiguredPin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("versioned release execution supports macOS and Linux")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := t.TempDir()
	t.Setenv("DOOMER_CONFIG_DIR", dir)
	first, second := newWatchTestRepo(t), newWatchTestRepo(t)
	putPin(t, first, "2026.11.3.7")
	putPin(t, second, "2026.11.4.1")
	putRuntime(t, home, "2026.11.3.7")
	cfg := watching.Config{Directories: []string{first, second}}
	if err := watching.Write(filepath.Join(dir, "watch", "profile", "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	if err := validateLauncherMigration(home); err == nil || !strings.Contains(err.Error(), second) {
		t.Fatal("did not protect unmigrated repository", err)
	}
	putRuntime(t, home, "2026.11.4.1")
	if err := validateLauncherMigration(home); err != nil {
		t.Fatal(err)
	}
	cfg.AllRepositories = true
	if err := compatibleRepositoryPins(home, cfg); err == nil {
		t.Fatal("expanded legacy all-repository scope")
	}
}
