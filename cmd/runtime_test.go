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
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
	"github.com/doomerlabs/doomer/internal/version"
	watching "github.com/doomerlabs/doomer/internal/watch"
)

func putRuntime(t *testing.T, home, release string) {
	t.Helper()
	path := runtimeversion.Binary(home, release)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("#!/bin/sh\nif [ \"$1\" = runtime-info ]; then printf '{\"protocol\":1}\\n'; else printf '" + release + " %s\\n' \"$DOOMER_SELECTED_PIN\"; printf '%s\\n' \"$@\"; fi\n")
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

func TestLauncherForwardsCommandsAndFlagsToSelectedRelease(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("versioned release execution supports macOS and Linux")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	original := launcherExecutable
	launcherExecutable = func() (string, error) { return filepath.Join(home, ".local/bin/doomer"), nil }
	t.Cleanup(func() { launcherExecutable = original })

	repo := newWatchTestRepo(t)
	release := "2026.11.5.5"
	putPin(t, repo, release)
	putRuntime(t, home, release)
	t.Chdir(repo)

	otherRepo := newWatchTestRepo(t)
	otherRelease := "2026.11.5.6"
	putPin(t, otherRepo, otherRelease)
	putRuntime(t, home, otherRelease)

	for _, tt := range []struct {
		name    string
		args    []string
		release string
	}{
		{"new command", []string{"future-command", "--new-option", "value"}, release},
		{"new subcommand", []string{"review", "future-command"}, release},
		{"new flag", []string{"version", "--new-option=value"}, release},
		{"changed flag", []string{"version", "--help=extended"}, release},
		{"reserved command as value", []string{"future-command", "--label", "self-update"}, release},
		{"runtime command as value", []string{"future-command", "--label", "runtime-info"}, release},
		{"hook flag after terminator", []string{"future-command", "--", "--dispatch"}, release},
		{"new dispatch flag", []string{"future-command", "--dispatch"}, release},
		{"explicit repo", []string{"future-command", "--repo", otherRepo}, otherRelease},
		{"explicit repo equals", []string{"future-command", "--repo=" + otherRepo}, otherRelease},
		{"explicit path", []string{"future-command", "--path", otherRepo}, otherRelease},
		{"new flag before repo", []string{"onboard", "--new-option", "value", "--repo", otherRepo}, otherRelease},
		{"new flag with directory", []string{"watch", "status", otherRepo, "--new-option=value"}, otherRelease},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			handled, err := DispatchCLI(context.Background(), tt.args, nil, &out, &out)
			if !handled || err != nil || !strings.HasPrefix(out.String(), tt.release+" ") {
				t.Fatalf("dispatch: handled=%v err=%v output=%q", handled, err, out.String())
			}
			if !strings.HasSuffix(out.String(), strings.Join(tt.args, "\n")+"\n") {
				t.Fatalf("arguments were not forwarded unchanged: %q", out.String())
			}
		})
	}
}

func TestLauncherKeepsLocalCommands(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	original := launcherExecutable
	launcherExecutable = func() (string, error) { return filepath.Join(home, ".local/bin/doomer"), nil }
	t.Cleanup(func() { launcherExecutable = original })
	repo := newWatchTestRepo(t)
	putPin(t, repo, "2026.11.5.5")
	t.Chdir(repo)
	for _, args := range [][]string{
		{"onboard", "reset", "--dry-run"},
		{"self-update", "--check"},
		{"runtime-info"},
		{"doctor", "--no-upload"},
		{"preflight", "--agent", "codex", "--repo", repo},
		{"watch", "hook", "--dispatch"},
		{"watch", "hook", "--dispatch=true"},
	} {
		var out bytes.Buffer
		handled, err := DispatchCLI(context.Background(), args, nil, &out, &out)
		if handled || err != nil {
			t.Fatalf("local command %v dispatched: handled=%v err=%v", args, handled, err)
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
	supervisor := filepath.Join(root, "supervisor")
	_ = watching.Write(filepath.Join(root, "config.json"), watching.Config{API: api, Profile: profile})
	supervisorLock, err := publock.TryAcquire(supervisor, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	defer supervisorLock.Close()
	_ = watching.Write(filepath.Join(supervisor, "process.json"), watchProcess{Session: version.Version, ManagedSession: "supervisor"})
	_ = watching.Write(filepath.Join(supervisor, "heartbeat.json"), time.Now())
	for _, release := range []string{"2026.11.3.7", "2026.11.4.1", "2026.11.5.1"} {
		putRuntime(t, home, release)
		path := runtimeversion.Binary(home, release)
		body := []byte("#!/bin/sh\ncat >/dev/null\nprintf '{\"decision\":\"block\",\"reason\":\"" + release + "\"}\\n'\n")
		os.WriteFile(path, body, 0755)
		sum := sha256.Sum256(body)
		raw, _ := json.Marshal(selfupdate.Record{SchemaVersion: 1, Manager: "doomer-review", Path: path, Version: release, SHA256: hex.EncodeToString(sum[:])})
		os.WriteFile(filepath.Join(filepath.Dir(path), "installation.json"), raw, 0600)
		runtimeRoot := filepath.Join(root, "runtimes", release)
		os.MkdirAll(runtimeRoot, 0700)
		_ = watching.Write(watching.TurnPath(runtimeRoot, "session"), watching.Turn{Owner: watching.Owner{Session: "session"}, Repositories: []string{"edited checkout"}})
		_ = watching.Write(filepath.Join(runtimeRoot, "activity.json"), time.Now())
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
	for _, test := range []struct {
		name             string
		brokenSupervisor bool
		conflictingPin   bool
		warning          string
	}{
		{name: "supervisor unavailable", brokenSupervisor: true, warning: "watcher supervision is unavailable"},
		{name: "unresolved checkout"},
		{name: "conflicting pins", conflictingPin: true, warning: "conflicting Codex and Claude CLI pins"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.brokenSupervisor {
				if err := os.WriteFile(filepath.Join(supervisor, "process.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				_ = watching.Write(filepath.Join(supervisor, "process.json"), watchProcess{Session: version.Version, ManagedSession: "supervisor"})
			}
			input := watching.HookInput{Session: "session", Event: "Stop", CWD: t.TempDir()}
			for _, release := range []string{"2026.11.3.7", "2026.11.4.1", "2026.11.5.1"} {
				_ = os.Remove(watching.TurnPath(filepath.Join(root, "runtimes", release), input.Session))
			}
			if test.conflictingPin {
				repo := newWatchTestRepo(t)
				putPin(t, repo, "2026.11.3.7")
				dir := filepath.Join(repo, ".claude/skills/doomer-review/references")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "install.md"), []byte("## Select CLI release 2026.11.4.1\n"), 0600); err != nil {
					t.Fatal(err)
				}
				input.CWD = repo
			}
			raw, _ := json.Marshal(input)
			out.Reset()
			c.SetIn(bytes.NewReader(raw))
			if err := dispatchRuntimeHooks(c, api, profile, root, "codex"); err != nil {
				t.Fatal(err)
			}
			result = nil
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result["decision"] != "block" || result["reason"] != "2026.11.3.7\n\n2026.11.4.1\n\n2026.11.5.1" {
				t.Fatal("lost runtime output", result)
			}
			if test.warning != "" && !strings.Contains(out.String(), test.warning) {
				t.Fatal("missing diagnostic", result)
			}
		})
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

func TestActiveRuntimeRepositoriesFollowsPinAndActivity(t *testing.T) {
	root, repo := t.TempDir(), newWatchTestRepo(t)
	cfg := watching.Config{RuntimeVersion: "2026.11.5.7", Directories: []string{repo}}
	putPin(t, repo, "2026.11.5.7")
	if active, err := activeRuntimeRepositories(root, cfg, time.Now()); err != nil || !active {
		t.Fatalf("matching explicit checkout lost: active=%v err=%v", active, err)
	}
	putPin(t, repo, "2026.11.5.8")
	if active, err := activeRuntimeRepositories(root, cfg, time.Now()); err != nil || active {
		t.Fatalf("upgraded checkout retained old runtime: active=%v err=%v", active, err)
	}
	putPin(t, repo, "2026.11.5.7")
	cfg.Directories = nil
	cfg.AllRepositories = true
	root = t.TempDir()
	if err := watching.RegisterRepository(root, repo, "agent", false, time.Now().Add(-15*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if active, err := activeRuntimeRepositories(root, cfg, time.Now()); err != nil || active {
		t.Fatalf("expired checkout retained runtime: active=%v err=%v", active, err)
	}
	if err := watching.RegisterRepository(root, repo, "agent", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if active, err := activeRuntimeRepositories(root, cfg, time.Now()); err != nil || !active {
		t.Fatalf("new hook registration lost: active=%v err=%v", active, err)
	}
}

func TestEnsureRuntimeWatchRegistersBeforeReusingWatcher(t *testing.T) {
	root, repo := t.TempDir(), newWatchTestRepo(t)
	lock, err := publock.TryAcquire(root, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := watching.Write(filepath.Join(root, "process.json"), watchProcess{ManagedSession: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := watching.Write(filepath.Join(root, "heartbeat.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ensureRuntimeWatch(context.Background(), root, repo, "https://example.test/api", "default"); err != nil {
		t.Fatal(err)
	}
	if active, err := activeRuntimeRepositories(root, watching.Config{AllRepositories: true}, time.Now()); err != nil || !active {
		t.Fatalf("reused watcher could retire before the hook registers: active=%v err=%v", active, err)
	}
}

func TestEnsureRuntimeWatchPreservesPause(t *testing.T) {
	root, repo := t.TempDir(), newWatchTestRepo(t)
	if err := watching.Write(filepath.Join(root, "paused.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ensureRuntimeWatch(context.Background(), root, repo, "https://example.test/api", "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "checkouts")); !os.IsNotExist(err) {
		t.Fatal("paused runtime registered a checkout")
	}
}
