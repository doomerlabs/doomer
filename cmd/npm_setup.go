package cmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type npmSetupOptions struct {
	home, executable, version, source, digest, packageVersion, node, npm, skillSource          string
	agents, claudeDecision, codexDecision, proposal, mode, defaultSkillSource, skillsNPMOption string
	noHooks, approveUploads, interactive                                                       bool
}

type npmSetupState map[string]json.RawMessage

func (s npmSetupState) get(key string, dst any) error {
	if value, ok := s[key]; ok {
		return json.Unmarshal(value, dst)
	}
	return nil
}

func (s npmSetupState) set(key string, value any) {
	// Values written here are JSON primitives or concrete configuration types.
	s[key], _ = json.Marshal(value)
}

func (s npmSetupState) consent() (agentHookState, error) {
	var state agentHookState
	raw, err := json.Marshal(s)
	if err == nil {
		err = json.Unmarshal(raw, &state)
	}
	return state, err
}

func readNPMSetupState(home string) (npmSetupState, error) {
	state := npmSetupState{"version": json.RawMessage("1"), "agents": json.RawMessage("[]"), "grants": json.RawMessage("[]")}
	err := watching.Read(filepath.Join(home, "state.json"), &state)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if state == nil || err != nil {
		return nil, fmt.Errorf("read installer state: %v", err)
	}
	var v int
	if state.get("version", &v) != nil || v != 1 || state["managed"] != nil {
		return nil, fmt.Errorf("unsupported personal installer state")
	}
	return state, nil
}

// Keep raw values, including the exact bytes of existing grants.
func writeNPMSetupState(home string, state npmSetupState) error {
	keys := make([]string, 0, len(state))
	for key := range state {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	var out bytes.Buffer
	out.WriteString("{\n")
	for i, key := range keys {
		if i > 0 {
			out.WriteString(",\n")
		}
		name, _ := json.Marshal(key)
		fmt.Fprintf(&out, "  %s: %s", name, state[key])
	}
	out.WriteString("\n}\n")
	return writeNPMFile(filepath.Join(home, "state.json"), out.Bytes(), 0600)
}

func writeNPMFile(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".doomer-install-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(content)
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func npmSetupLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.Mkdir(path+".lock", 0700); err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { _ = os.Remove(path + ".lock") }, nil
}

func newNPMSetupCommand() *cobra.Command {
	var o npmSetupOptions
	c := &cobra.Command{Use: "npm-setup", Short: "Run the native installer from the npm launcher", Hidden: true, Args: cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		PersistentPostRun: func(*cobra.Command, []string) {},
		RunE:              func(c *cobra.Command, _ []string) error { return runNPMSetup(c, o) },
	}
	f := c.Flags()
	f.StringVar(&o.mode, "mode", "", "install, onboard or update")
	f.StringVar(&o.home, "manager-home", "", "installer state directory")
	f.StringVar(&o.executable, "executable", "", "stable CLI path")
	f.StringVar(&o.version, "release-version", "", "verified native version")
	f.StringVar(&o.source, "release-source", "", "verified native source")
	f.StringVar(&o.digest, "release-sha256", "", "verified native SHA-256")
	f.StringVar(&o.packageVersion, "package-version", "", "npm package version")
	f.StringVar(&o.node, "node", "", "bootstrap Node executable")
	f.StringVar(&o.npm, "npm", "", "npm CLI script")
	f.StringVar(&o.defaultSkillSource, "default-skill-source", "", "pinned skill source")
	f.StringVar(&o.skillsNPMOption, "skills-npm-option", "", "skills CLI npm package or fixture prefix")
	f.StringVar(&o.skillSource, "skill-source", "", "local skill checkout")
	f.StringVar(&o.agents, "agent", "", "comma-separated agent selection")
	f.StringVar(&o.claudeDecision, "claude-review-permission", "", "scoped Claude permission decision")
	f.StringVar(&o.codexDecision, "reviewer-policy", "", "scoped Codex policy decision")
	f.StringVar(&o.proposal, "reviewer-policy-proposal", "", "reviewed Codex proposal digest")
	f.BoolVar(&o.noHooks, "no-hooks", false, "CLI-only installation")
	f.BoolVar(&o.approveUploads, "approve-uploads", false, "approve Doomer uploads")
	f.BoolVar(&o.interactive, "interactive", false, "bootstrap has an interactive terminal")
	return c
}

type npmSetupPrompt struct {
	input  *bufio.Reader
	output io.Writer
}

func setupAgents(c *cobra.Command, o npmSetupOptions, saved []string, p npmSetupPrompt) ([]string, error) {
	if o.mode == "update" {
		return saved, nil
	}
	if o.noHooks {
		return []string{}, nil
	}
	if c.Flags().Changed("agent") {
		selected := []string{}
		for _, agent := range strings.Split(o.agents, ",") {
			agent = strings.TrimSpace(agent)
			if agent == "" {
				continue
			}
			if agent != "claude" && agent != "codex" {
				return nil, fmt.Errorf("supported agents are claude and codex")
			}
			if !slices.Contains(selected, agent) {
				selected = append(selected, agent)
			}
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("supported agents are claude and codex; use --no-hooks for CLI-only setup")
		}
		return selected, nil
	}
	if len(saved) == 0 {
		for _, agent := range []string{"claude", "codex"} {
			if _, err := exec.LookPath(agent); err == nil {
				saved = append(saved, agent)
			}
		}
	}
	if !o.interactive {
		if len(saved) == 0 {
			return nil, fmt.Errorf("no supported agents detected; choose --agent claude,codex or --no-hooks")
		}
		return saved, nil
	}
	return pickNPMSetupAgents(c, saved, p)
}

var errNPMSetupCanceled = errors.New("setup canceled")

func runNPMSetup(c *cobra.Command, o npmSetupOptions) error {
	if !filepath.IsAbs(o.home) || !filepath.IsAbs(o.executable) || !slices.Contains([]string{"install", "onboard", "update"}, o.mode) {
		return fmt.Errorf("invalid npm setup invocation")
	}
	unlock, err := npmSetupLock(filepath.Join(o.home, "install"))
	if err != nil {
		return err
	}
	defer unlock()
	state, err := readNPMSetupState(o.home)
	if err != nil {
		return err
	}
	old, err := state.consent()
	if err != nil {
		return err
	}
	var saved []string
	if err := state.get("agents", &saved); err != nil {
		return err
	}
	for _, agent := range saved {
		if agent != "codex" && agent != "claude" {
			return fmt.Errorf("invalid saved agent %q", agent)
		}
	}
	in, isFile := c.InOrStdin().(*os.File)
	o.interactive = o.interactive && isFile && term.IsTerminal(int(in.Fd()))
	p := npmSetupPrompt{bufio.NewReader(c.InOrStdin()), c.ErrOrStderr()}
	selected, err := setupAgents(c, o, saved, p)
	if errors.Is(err, errNPMSetupCanceled) {
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]string{"status": "canceled"})
	}
	if err != nil {
		return err
	}
	if o.claudeDecision != "" && !slices.Contains(selected, "claude") {
		return fmt.Errorf("--claude-review-permission requires selecting claude")
	}
	if o.codexDecision != "" && !slices.Contains(selected, "codex") {
		return fmt.Errorf("--reviewer-policy requires selecting codex")
	}
	if (o.claudeDecision != "" && o.claudeDecision != "allow" && o.claudeDecision != "decline") ||
		(o.codexDecision != "" && o.codexDecision != "allow" && o.codexDecision != "decline") {
		return fmt.Errorf("invalid reviewer decision")
	}
	if o.skillSource != "" {
		if !filepath.IsAbs(o.skillSource) || !npmSkillExists(filepath.Join(o.skillSource, "skills", "doomer", "SKILL.md")) {
			return fmt.Errorf("--skill-source must be an absolute local checkout containing skills/doomer/SKILL.md")
		}
	}

	directory, err := os.Getwd()
	if err != nil {
		return err
	}
	env, err := directoryWatchEnvironment(directory)
	if err != nil {
		return err
	}
	explicit, _ := c.Flags().GetString("api-url")
	api, err := agentHookAPI(explicit, env.get("DOOMER_API_URL"), old.API)
	if err != nil {
		return err
	}
	subprocess, err := os.Executable()
	if err != nil {
		return err
	}

	grant := o.mode != "update" && o.approveUploads
	browser := false
	approved := slices.ContainsFunc(old.Grants, func(g agentUploadGrant) bool {
		return g.Scope == "global" && g.API == api && !g.ApprovedAt.IsZero()
	})
	if o.mode != "update" && !approved && !grant && len(selected) > 0 {
		if !o.interactive {
			return fmt.Errorf("Upload approval is required; run setup in a terminal or pass --approve-uploads if you approve the uploads")
		}
		origin := strings.SplitN(api, "/", 4)
		fmt.Fprintf(p.output, "Doomer sends source snapshots, task context, and finding decisions with explanations to %s for all repositories it reviews.\n", strings.Join(origin[:3], "/"))
		fmt.Fprintln(p.output, "If you’re using SSH, open the printed link on another device.")
		answer, err := p.choose(c, "Connect your Doomer account", []npmChoice{
			{"browser", "Open browser"}, {"exit", "No, exit setup"},
		}, 0, nil)
		if err != nil && !errors.Is(err, errNPMSetupCanceled) {
			return err
		}
		if err != nil || answer != "browser" {
			return json.NewEncoder(c.OutOrStdout()).Encode(map[string]string{"status": "canceled"})
		}
		browser, grant = true, true
	}

	credentialDir := env.get("DOOMER_CONFIG_DIR")
	if browser {
		if env.get("DOOMER_TOKEN") != "" || strings.TrimSpace(os.Getenv("DOOMER_TOKEN")) != "" {
			return fmt.Errorf("unset DOOMER_TOKEN before browser approval")
		}
		routed, err := agentHookAPI("", env.get("DOOMER_API_URL"), old.API)
		if err != nil {
			return err
		}
		if routed != api && env.get("DOOMER_API_URL") != "" {
			return fmt.Errorf("this directory routes Doomer reviews to a different service; check .doomer.env")
		}
		dir, profile, err := watchCredentialProfile(c, api, old, env)
		if err != nil {
			return err
		}
		credentialDir = dir
		login := exec.CommandContext(c.Context(), subprocess, "--api-url", api, "--profile", profile, "login", "--device", "--review-uploads")
		login.Env = append(os.Environ(), "DOOMER_CONFIG_DIR="+dir)
		login.Stdin, login.Stdout, login.Stderr = c.InOrStdin(), p.output, p.output
		if err := login.Run(); err != nil {
			return fmt.Errorf("browser approval failed: %w", err)
		}
	}
	settings, err := npmAgentSettings(o, selected)
	if err != nil {
		return err
	}
	if err := settings.validate(); err != nil {
		return err
	}
	installedSkills := false
	if o.mode != "onboard" || !settings.skillPresent() || o.skillSource != "" {
		if err := installNPMSkill(c.Context(), o, selected); err != nil {
			return err
		}
		installedSkills = true
	}
	if !settings.skillPresent() {
		return fmt.Errorf("the skills installer did not install Doomer for the selected agents")
	}

	unlockState, err := npmSetupLock(filepath.Join(o.home, "state.json"))
	if err != nil {
		return err
	}
	// A revoke or another maintenance operation during the prompt wins.
	current, err := readNPMSetupState(o.home)
	if err == nil && !equalNPMSetupState(state, current) {
		err = fmt.Errorf("installer state changed during setup; retry to use the latest choices")
	}
	if err == nil {
		err = commitNPMSetup(o, state, old, selected, api, credentialDir, grant, approved, installedSkills, settings, subprocess)
	}
	unlockState()
	if err != nil {
		return err
	}

	if slices.Contains(selected, "claude") {
		if err := settings.claudePermission(c, o, p); err != nil {
			return err
		}
	}
	if slices.Contains(selected, "codex") {
		err = npmCodexPermissions(c, o, settings.files["codex"], api, state, p)
		if err != nil {
			return err
		}
	}
	return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{
		"status": "configured", "agents": selected,
	})
}

func equalNPMSetupState(a, b npmSetupState) bool {
	if len(a) != len(b) {
		return false
	}
	for key, v := range a {
		if !bytes.Equal(v, b[key]) {
			return false
		}
	}
	return true
}

func commitNPMSetup(o npmSetupOptions, state npmSetupState, old agentHookState, selected []string, api, credentialDir string, grant, approved, installedSkills bool, settings npmSettings, binary string) error {
	if grant && !approved {
		var grants []json.RawMessage
		if err := state.get("grants", &grants); err != nil {
			return err
		}
		grants = slices.DeleteFunc(grants, func(raw json.RawMessage) bool {
			var g agentUploadGrant
			return json.Unmarshal(raw, &g) == nil && g.API == api
		})
		raw, err := json.Marshal(agentUploadGrant{Scope: "global", API: api, ApprovedAt: time.Now().UTC().Truncate(time.Millisecond)})
		if err != nil {
			return err
		}
		state.set("grants", append(grants, raw))
	}
	if grant || approved {
		if old.Identities[api] == "" {
			if old.Identities == nil {
				old.Identities = make(map[string]string)
			}
			var entropy [32]byte
			rand.Read(entropy[:])
			old.Identities[api] = "doomer_anon_" + base64.RawURLEncoding.EncodeToString(entropy[:])
			state.set("identities", old.Identities)
		}
		if credentialDir != "" && (grant && !approved || old.CredentialDirs[api] == "") && o.mode != "update" {
			dir, err := filepath.Abs(credentialDir)
			if err != nil {
				return err
			}
			if old.CredentialDirs == nil {
				old.CredentialDirs = make(map[string]string)
			}
			old.CredentialDirs[api] = dir
			state.set("credentialDirs", old.CredentialDirs)
		}
	}
	if o.mode != "update" {
		state.set("api", api)
	}
	state.set("installer", "go")
	state.set("cliVersion", o.packageVersion)
	var configured []string
	if err := state.get("agents", &configured); err != nil {
		return err
	}
	state.set("agents", appendUnique(configured, selected...))
	if installedSkills {
		state.set("skillInstalledAt", time.Now().UTC())
		var paths []string
		if err := state.get("skillPaths", &paths); err != nil {
			return err
		}
		state.set("skillPaths", appendUnique(paths, settings.skillDirs...))
	}
	path, err := activateNPMBinary(o, binary)
	if err != nil {
		return err
	}
	state.set("native", map[string]any{"path": path, "version": o.version, "source": o.source, "sha256": o.digest, "watch": true})
	state.set("executable", o.executable)
	binaries := map[string]string{}
	if err := state.get("binaries", &binaries); err != nil {
		return err
	}
	if binaries == nil {
		binaries = map[string]string{}
	}
	binaries[path] = o.digest
	state.set("binaries", binaries)

	if err := os.Remove(filepath.Join(o.home, "uninstalling.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writeNPMSetupState(o.home, state); err != nil {
		return err
	}
	// Settings have their own lock. Record each attempted hook file first so an
	// interrupted setup can be repaired or uninstalled.
	return settings.installHooks(o, state)
}

func activateNPMBinary(o npmSetupOptions, binary string) (string, error) {
	data, err := os.ReadFile(binary)
	if err != nil {
		return "", err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != o.digest {
		return "", fmt.Errorf("verified native CLI checksum changed")
	}
	suffix := o.version
	if o.source == "local" {
		suffix = "dev"
	}
	if suffix == "" || strings.ContainsAny(suffix, "/\\:\r\n") || suffix == "." || suffix == ".." {
		return "", fmt.Errorf("invalid native CLI version")
	}
	dir := filepath.Dir(o.executable)
	path := filepath.Join(dir, "doomer-"+suffix)
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	before, err := os.Lstat(path)
	switch {
	case err == nil && !before.Mode().IsRegular():
		return "", fmt.Errorf("native CLI destination is not a regular file")
	case err == nil && o.source != "local":
		previous, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if fmt.Sprintf("%x", sha256.Sum256(previous)) != o.digest {
			return "", fmt.Errorf("installed version differs at %s", path)
		}
	case err == nil || errors.Is(err, os.ErrNotExist):
		if err := writeNPMFile(path, data, 0755); err != nil {
			return "", err
		}
	default:
		return "", err
	}
	if path == o.executable {
		return path, nil
	}
	link := filepath.Join(dir, ".doomer-"+rand.Text())
	defer os.Remove(link)
	if err := os.Symlink(filepath.Base(path), link); err != nil {
		return "", err
	}
	return path, os.Rename(link, o.executable)
}

func appendUnique(existing []string, items ...string) []string {
	for _, item := range items {
		if !slices.Contains(existing, item) {
			existing = append(existing, item)
		}
	}
	return existing
}

func npmSkillExists(file string) bool {
	data, err := os.ReadFile(file)
	if err != nil {
		return false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && k == "name" && strings.TrimSpace(v) == "doomer" {
			return true
		}
	}
	return false
}

func installNPMSkill(ctx context.Context, o npmSetupOptions, agents []string) error {
	source := o.skillSource
	if source == "" {
		// Match the reviewed revision pinned by the npm installer.
		revision, ok := strings.CutPrefix(o.defaultSkillSource, "doomerlabs/skills#")
		if filepath.IsAbs(o.defaultSkillSource) {
			return execNPMSkill(ctx, o, agents, o.defaultSkillSource)
		}
		if !ok || len(revision) != 40 || strings.IndexFunc(revision, func(r rune) bool {
			return !strings.ContainsRune("0123456789abcdef", r)
		}) >= 0 {
			return fmt.Errorf("invalid pinned Doomer skill revision")
		}
		temp, err := os.MkdirTemp("", "doomer-skill-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temp)
		source = filepath.Join(temp, "checkout")
		for _, args := range [][]string{
			{"init", "--quiet", source},
			{"-C", source, "fetch", "--quiet", "--depth=1", "https://github.com/doomerlabs/skills.git", revision},
			{"-C", source, "checkout", "--quiet", "--detach", "FETCH_HEAD"},
		} {
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=" + os.DevNull}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
			err := cmd.Run()
			cancel()
			if err != nil {
				return fmt.Errorf("fetch pinned Doomer skill: %w", err)
			}
		}
		head, err := exec.CommandContext(ctx, "git", "-C", source, "rev-parse", "HEAD").Output()
		if err != nil || strings.TrimSpace(string(head)) != revision {
			return fmt.Errorf("downloaded skill does not match the pinned revision")
		}
	}
	return execNPMSkill(ctx, o, agents, source)
}

func execNPMSkill(ctx context.Context, o npmSetupOptions, agents []string, source string) error {
	if o.skillsNPMOption != "--package=skills" && !filepath.IsAbs(strings.TrimPrefix(o.skillsNPMOption, "--prefix=")) {
		return fmt.Errorf("invalid upstream skills npm option")
	}
	args := []string{"exec", "--yes", o.skillsNPMOption, "--", "skills", "add", source, "--skill", "doomer", "--global", "--yes", "--agent"}
	for _, agent := range agents {
		if agent == "claude" {
			agent = "claude-code"
		}
		args = append(args, agent)
	}
	if len(agents) == 0 {
		args = append(args, "codex")
	}
	name := "npm"
	if o.npm != "" && filepath.IsAbs(o.npm) && filepath.IsAbs(o.node) &&
		slices.Contains([]string{".js", ".mjs", ".cjs"}, filepath.Ext(o.npm)) {
		name, args = o.node, append([]string{o.npm}, args...)
	} else if runtime.GOOS == "windows" {
		return fmt.Errorf("run setup through npx so the npm CLI is available")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, name, args...).Run(); err != nil {
		return fmt.Errorf("install the Doomer skill: %w", err)
	}
	return nil
}
