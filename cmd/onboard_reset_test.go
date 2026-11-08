package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/pelletier/go-toml/v2"
)

func resetFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func resetContents(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestResetDoomerCommand(t *testing.T) {
	for _, cmd := range []string{"'/opt/doomer' --profile default agent-hook --managed-task-adapter", "env DOOMER_DEV_CLI=1 '/my path/doomer' agent-hook --managed-task-adapter", "doomer watch hook --state '/foo' 2>/dev/null || printf '{}'"} {
		if !resetDoomerCommand(cmd) {
			t.Errorf("missed %s", cmd)
		}
	}
	for _, cmd := range []string{"echo 'doomer watch hook'", "other --state doomer", "git status # doomer watch hook"} {
		if resetDoomerCommand(cmd) {
			t.Errorf("matched %s", cmd)
		}
	}
}
func TestOnboardResetDiscoversAndPreserves(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "state")
	t.Setenv("HOME", home)
	t.Setenv("DOOMER_CONFIG_DIR", config)
	codex := filepath.Join(home, ".codex")
	extra := filepath.Join(home, "custom-codex")
	profile := filepath.Join(home, "Library/Application Support/Codex Accounts/profiles/abc/home")
	claude := filepath.Join(home, ".claude")
	hooks := `{"unrelated":true,"hooks":{"SessionStart":[{"hooks":[{"command":"'/opt/doomer' agent-hook --managed-task-adapter"},{"command":"echo keep"}]}]}}`
	for _, dir := range []string{codex, extra, profile} {
		resetFixture(t, filepath.Join(dir, "hooks.json"), hooks)
		resetFixture(t, filepath.Join(dir, "auth.json"), "login secret")
	}
	resetFixture(t, filepath.Join(claude, "settings.json"), hooks)
	resetFixture(t, filepath.Join(home, ".claude.json"), "claude login secret")
	policy := proposedCodexReviewPolicy(managedConsentEvidence{Account: "a", Service: "s", Profile: "p", Agent: "codex", AllRepositories: true}).ExtraPolicy
	cfg := []byte("# preserved comment\nmodel = 'keep'\n\n[auto_review]\nextra_policy = 'keep policy'\n")
	cfg, err := patchCodexExtraPolicy(cfg, "keep policy\n"+policy)
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte("\n[hooks.state.\""+filepath.Join(codex, "hooks.json")+":session_start:0:0\"]\ntrusted_hash='remove'\n[hooks.state.\"unrelated\"]\ntrusted_hash='keep'\n")...)
	resetFixture(t, filepath.Join(codex, "config.toml"), string(cfg))
	resetFixture(t, filepath.Join(config, "auth.json"), "doomer login secret")
	state := filepath.Join(config, "managed")
	resetFixture(t, filepath.Join(state, "integration-consents/a.json"), `{"approved":true,"agent":"codex"}`)
	resetFixture(t, filepath.Join(state, "consents/a.json"), `{"approved":true}`)
	resetFixture(t, filepath.Join(state, "update-authorizations/a.json"), `{"authorized":true}`)
	resetFixture(t, filepath.Join(state, "selections/a.json"), `{"authorized":true,"release":{"version":"keep"}}`)
	repo := filepath.Join(home, "repo")
	handle := strings.Repeat("a", 48)
	task := managed.Task{Schema: managed.Protocol, Handle: handle, Repo: repo, RepositoryID: filepath.Join(repo, ".git"), HookActivated: true}
	taskData, _ := json.Marshal(task)
	resetFixture(t, filepath.Join(state, "tasks", handle+".json"), string(taskData))
	resetFixture(t, filepath.Join(repo, "AGENTS.md"), "before\n<!-- doomer-review:start -->\nmanaged instructions\n<!-- doomer-review:end -->\nafter\n")
	portable := portableSetup{Schema: 1, Authorized: true, Grant: repositoryConsent{Approved: true, At: time.Now()}}
	portable.Auth.Token = "preserved"
	portableData, _ := json.Marshal(portable)
	portableFile := filepath.Join(repo, ".git/doomer/portable/a.json")
	resetFixture(t, portableFile, string(portableData))
	getenv := func(k string) string {
		switch k {
		case "CODEX_HOME":
			return extra
		case "DOOMER_CONFIG_DIR":
			return config
		}
		return ""
	}
	p := discoverReset(home, getenv)
	// Planning must not mutate anything.
	if resetContents(t, filepath.Join(codex, "hooks.json")) != hooks {
		t.Fatal("discovery mutated hooks")
	}
	p.run(context.Background())
	for _, e := range p.Entries {
		if e.Error != "" {
			t.Errorf("%s: %s", e.Path, e.Error)
		}
	}
	for _, dir := range []string{codex, extra, profile} {
		if strings.Contains(resetContents(t, filepath.Join(dir, "hooks.json")), "/opt/doomer") {
			t.Error("hook remains")
		}
		if resetContents(t, filepath.Join(dir, "auth.json")) != "login secret" {
			t.Error("login changed")
		}
	}
	for _, file := range []string{filepath.Join(config, "auth.json"), filepath.Join(home, ".claude.json")} {
		if !strings.Contains(resetContents(t, file), "login secret") {
			t.Error("credential changed")
		}
	}
	result := resetContents(t, filepath.Join(codex, "config.toml"))
	if !strings.Contains(result, "# preserved comment") || !strings.Contains(result, "keep policy") || strings.Contains(result, "trusted_hash = 'remove'") || strings.Contains(result, "Doomer repository") {
		t.Fatalf("bad config %s", result)
	}
	var obj map[string]any
	if err := toml.Unmarshal([]byte(result), &obj); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resetContents(t, filepath.Join(repo, "AGENTS.md")), "doomer-review") {
		t.Fatal("instructions remain")
	}
	var saved portableSetup
	if err := json.Unmarshal([]byte(resetContents(t, portableFile)), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Grant.Approved || saved.Authorized || saved.Auth.Token != "preserved" {
		t.Fatal("portable grant or credential")
	}
	var grant map[string]any
	json.Unmarshal([]byte(resetContents(t, filepath.Join(state, "consents/a.json"))), &grant)
	if grant["approved"] != false {
		t.Fatal("grant remains")
	}
	// A repeat reset is safe and preserves the same user-owned settings.
	again := discoverReset(home, getenv)
	again.run(context.Background())
	for _, e := range again.Entries {
		if e.Error != "" {
			t.Errorf("repeat: %s", e.Error)
		}
	}
	if resetContents(t, filepath.Join(codex, "config.toml")) != result {
		t.Fatal("repeat rewrote settings")
	}
}
func TestResetRejectsSymlinksAndMalformedSettings(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "target.json")
	resetFixture(t, target, `{"hooks":{}}`)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".codex/hooks.json")); err != nil {
		t.Fatal(err)
	}
	resetFixture(t, filepath.Join(home, ".claude/settings.json"), "invalid json")
	p := &resetPlan{}
	p.discoverAgent("Codex", filepath.Join(home, ".codex"))
	p.discoverAgent("Claude", filepath.Join(home, ".claude"))
	n := 0
	for _, e := range p.Entries {
		if e.Error != "" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("expected failures: %+v", p.Entries)
	}
	if resetContents(t, target) != `{"hooks":{}}` {
		t.Fatal("symlink target changed")
	}
}
func TestResetCLIWorksOutsideRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOOMER_CONFIG_DIR", filepath.Join(home, "state"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	root := newRootCommand(nil)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"onboard", "reset", "--dry-run", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"dry_run":true`) {
		t.Fatal(out.String())
	}
}

func TestResetInvalidatesOldGrantsButAllowsFreshOnboarding(t *testing.T) {
	state := managed.Store{Root: t.TempDir()}
	task := managed.Task{AccountID: "a", API: "https://example.test", Profile: "default", Agent: "codex", RepositoryID: "repo"}
	if err := recordIntegrationConsent(state, task, "test", true); err != nil {
		t.Fatal(err)
	}
	if err := watch.Write(filepath.Join(state.Root, "reset.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	evidence, err := savedManagedConsentEvidence(state, task)
	if err != nil || evidence != nil {
		t.Fatal("old grant survived", evidence, err)
	}
	if err := recordIntegrationConsent(state, task, "fresh", true); err != nil {
		t.Fatal(err)
	}
	evidence, err = savedManagedConsentEvidence(state, task)
	if err != nil || evidence == nil {
		t.Fatal("fresh grant rejected", err)
	}
}
func TestResetTOMLHooksPreserveUnrelatedHooksAndSettings(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "config.toml")
	resetFixture(t, file, `# top
model='keep'
[[hooks.Stop]]
[[hooks.Stop.hooks]]
command="doomer watch hook --state /x"
type='command'
[[hooks.Stop.hooks]]
command='echo keep'
type='command'
[hooks.state."`+file+`:stop:0:0"]
trusted_hash='remove'
[unrelated]
value = 'preserve'
`)
	p := &resetPlan{}
	p.discoverAgent("Codex", home)
	for _, e := range p.Entries {
		if e.Error != "" {
			t.Fatal(e.Error)
		}
		if e.apply != nil {
			if err := e.apply(); err != nil {
				t.Fatal(err)
			}
		}
	}
	data := resetContents(t, file)
	if strings.Contains(data, "doomer watch") || strings.Contains(data, "remove") || !strings.Contains(data, "echo keep") || !strings.Contains(data, "value = 'preserve'") {
		t.Fatal(data)
	}
}
func TestResetDoesNotFollowStateDirectorySymlink(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	config := filepath.Join(home, "state")
	resetFixture(t, filepath.Join(outside, "consents/x.json"), `{"approved":true}`)
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(config, "managed")); err != nil {
		t.Fatal(err)
	}
	p := &resetPlan{states: map[string]bool{config: true}, repos: map[string]bool{}}
	p.discoverState(config)
	p.run(context.Background())
	if resetContents(t, filepath.Join(outside, "consents/x.json")) != `{"approved":true}` {
		t.Fatal("changed symlink target")
	}
	if _, err := os.Stat(filepath.Join(outside, "reset.json")); !os.IsNotExist(err) {
		t.Fatal("wrote outside state")
	}
}
