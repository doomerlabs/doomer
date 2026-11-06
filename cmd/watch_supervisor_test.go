package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
	watching "github.com/doomerlabs/doomer/internal/watch"
)

func TestSupervisorRetiresLegacyRuntimeDespiteExplicitCheckout(t *testing.T) {
	shared := t.TempDir()
	root := filepath.Join(shared, "runtimes", "2026.11.5.6")
	if err := watching.RegisterRepository(root, t.TempDir(), "explicit", true, time.Now()); err != nil {
		t.Fatal(err)
	}
	lock, err := publock.TryAcquire(root, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := watching.Write(filepath.Join(root, "process.json"), watchProcess{ManagedSession: "old-runtime"}); err != nil {
		t.Fatal(err)
	}
	if err := watching.Write(filepath.Join(root, "heartbeat.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	firstSeen := map[string]time.Time{root: now.Add(-3 * time.Minute)}
	if err := reconcileIdleRuntimes(context.Background(), shared, firstSeen, now); err != nil {
		t.Fatal(err)
	}
	if !idleRuntime(root) || userPaused(root) {
		t.Fatal("old worker was not automatically suspended")
	}
	// The pause file is the existing protocol understood by releases before
	// the supervisor existed. Their normal cancellation loop observes it.
	if !paused(root) {
		t.Fatal("legacy worker cannot see the stop request")
	}
}

func TestLegacyWatcherProcess(t *testing.T) {
	root := os.Getenv("DOOMER_TEST_LEGACY_WATCH_ROOT")
	if root == "" {
		t.Skip("subprocess fixture")
	}
	lock, err := publock.TryAcquire(root, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	_ = watching.Write(filepath.Join(root, "process.json"), watchProcess{PID: os.Getpid(), ManagedSession: "legacy"})
	for !paused(root) {
		_ = watching.Write(filepath.Join(root, "heartbeat.json"), time.Now())
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSupervisorStopsLegacyProcessWithoutSignals(t *testing.T) {
	shared := t.TempDir()
	root := filepath.Join(shared, "runtimes", "2026.11.5.6")
	child := exec.Command(os.Args[0], "-test.run=^TestLegacyWatcherProcess$", "-test.timeout=10s")
	child.Env = append(os.Environ(), "DOOMER_TEST_LEGACY_WATCH_ROOT="+root)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitWatch(ctx, func() (bool, error) { _, running, err := runningWatch(root); return running, err }); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := reconcileIdleRuntimes(ctx, shared, map[string]time.Time{root: now.Add(-3 * time.Minute)}, now); err != nil {
		t.Fatal(err)
	}
	if err := resumeIdleRuntime(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatalf("legacy process did not exit gracefully: %v", err)
	}
	if _, running, err := runningWatch(root); err != nil || running {
		t.Fatal("legacy worker still owns its lock", running, err)
	}
	if paused(root) {
		t.Fatal("automatic suspension was not cleared after the old worker exited")
	}
}

func TestSupervisorPreservesForegroundAndUserPause(t *testing.T) {
	for _, foreground := range []bool{false, true} {
		t.Run(map[bool]string{false: "user pause", true: "foreground"}[foreground], func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "runtimes", "2026.11.5.6")
			if foreground {
				lock, err := publock.TryAcquire(root, "watcher")
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				_ = watching.Write(filepath.Join(root, "process.json"), watchProcess{})
				_ = watching.Write(filepath.Join(root, "heartbeat.json"), time.Now())
			} else {
				_ = watching.Write(filepath.Join(root, "paused.json"), "user decision")
			}
			if err := suspendIdleRuntime(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			if idleRuntime(root) {
				t.Fatal("replaced foreground or user-controlled state")
			}
		})
	}
}

func TestReadOnlyHooksStayIdleAndMutationResumes(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtimes", "2026.11.5.9")
	input := watching.HookInput{Session: "session", Event: "PreToolUse", Tool: "Bash", Input: json.RawMessage(`{"command":"git status --short"}`)}
	if err := prepareHookRuntime(context.Background(), root, input, true); err != nil {
		t.Fatal(err)
	}
	if !idleRuntime(root) {
		t.Fatal("read-only hook left legacy startup enabled")
	}
	input.Input = json.RawMessage(`{"command":"echo changed > file.txt"}`)
	if err := prepareHookRuntime(context.Background(), root, input, false); err != nil {
		t.Fatal(err)
	}
	if !idleRuntime(root) {
		t.Fatal("another runtime's edit resumed this worker")
	}
	if err := prepareHookRuntime(context.Background(), root, input, true); err != nil {
		t.Fatal(err)
	}
	if paused(root) || !runtimeHasWork(root, time.Now()) {
		t.Fatal("edit did not renew and resume its runtime")
	}
	_ = watching.Write(filepath.Join(sharedWatchRoot(root), "paused.json"), "user stop")
	if err := suspendIdleRuntime(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := prepareHookRuntime(context.Background(), root, input, true); err != nil {
		t.Fatal(err)
	}
	if !userPaused(root) {
		t.Fatal("edit resumed a user-paused profile")
	}
}

func TestRuntimeWorkKeepsPendingReviewsAndExpiresStaleState(t *testing.T) {
	root, repo := t.TempDir(), t.TempDir()
	now := time.Now()
	state := watching.State{Owner: watching.Owner{Session: "session", Turn: "turn"}, Fingerprint: "digest", RequestedAt: now}
	if err := watching.Write(watching.StatePath(root, repo), state); err != nil {
		t.Fatal(err)
	}
	if !runtimeHasWork(root, now) {
		t.Fatal("pending review lost its worker")
	}
	feed := watching.Feed{Owner: state.Owner, Fingerprint: state.Fingerprint, AssessmentStatus: "analyzing"}
	_ = watching.Write(watching.FeedPath(root, repo), feed)
	if !runtimeHasWork(root, now) {
		t.Fatal("in-flight review lost its worker")
	}
	feed.AssessmentStatus = "ready"
	_ = watching.Write(watching.FeedPath(root, repo), feed)
	if runtimeHasWork(root, now) {
		t.Fatal("completed review kept worker alive")
	}
	state.Active = map[string]watching.Operation{"edit": {Attributable: true, Started: now}}
	_ = watching.Write(watching.StatePath(root, repo), state)
	if !runtimeHasWork(root, now) {
		t.Fatal("unfinished edit lost its worker")
	}
	if runtimeHasWork(root, now.Add(31*time.Minute)) {
		t.Fatal("abandoned edit kept worker forever")
	}
}

func TestIdleResumeDoesNotClearChangedOrSharedPause(t *testing.T) {
	for _, shared := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "runtimes", "2026.11.5.9")
		if err := suspendIdleRuntime(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		pauseRoot := root
		if shared {
			pauseRoot = sharedWatchRoot(root)
		}
		_ = watching.Write(filepath.Join(pauseRoot, "paused.json"), "explicit stop after suspension")
		if err := resumeIdleRuntime(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		if !userPaused(root) {
			t.Fatal("cleared a later user decision")
		}
	}
}

func TestPromptReplayBeforeFirstEditAndOnlyOnce(t *testing.T) {
	shared := t.TempDir()
	root := filepath.Join(shared, "runtimes", "2026.11.5.9")
	calls := filepath.Join(shared, "calls")
	binary := filepath.Join(shared, "hook")
	// The fake protocol-1 hook records its input and returns an empty response.
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncat >> '"+calls+"'\nprintf '\\n' >> '"+calls+"'\nprintf '{}\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	prompt := runtimePrompt{At: time.Now(), Input: watching.HookInput{Session: "session", Turn: "turn", Event: "UserPromptSubmit", Prompt: "original task outside Git"}}
	_ = watching.Write(filepath.Join(shared, "runtime-prompts", watching.Key("session")+".json"), prompt)
	input := watching.HookInput{Session: "session", Turn: "turn", Event: "PreToolUse", Tool: "Edit"}
	raw, _ := json.Marshal(input)
	for range 2 {
		if _, err := runtimeHookOutput(context.Background(), binary, "https://example.test/api", "default", root, shared, "codex", input, raw); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "original task outside Git") || strings.Count(string(data), "original task outside Git") != 1 {
		t.Fatalf("prompt delivery order or count incorrect: %s", data)
	}
}

func TestRuntimeRoutingPreservesCrossCheckoutStop(t *testing.T) {
	root := t.TempDir()
	input := watching.HookInput{Session: "session", Event: "Stop"}
	if hookRuntimeSelected(root, "2026.11.5.6", "2026.11.5.9", input) {
		t.Fatal("unrelated runtime selected")
	}
	_ = watching.Write(watching.TurnPath(root, "session"), watching.Turn{Owner: watching.Owner{Session: "session"}, Repositories: []string{"previous checkout"}})
	if !hookRuntimeSelected(root, "2026.11.5.6", "2026.11.5.9", input) {
		t.Fatal("previous checkout cannot finish its turn")
	}
}

func TestIdleRuntimeResumesForUndeliveredCompletion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtimes", "2026.11.5.9")
	if err := suspendIdleRuntime(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	owner := watching.Owner{Session: "session", Turn: "turn"}
	repo := t.TempDir()
	turn := watching.Turn{Owner: owner, Repositories: []string{repo}}
	_ = watching.Write(watching.TurnPath(root, owner.Session), turn)
	_ = watching.Write(watching.FeedPath(root, repo), watching.Feed{Owner: owner, ReviewID: "review", AssessmentStatus: "ready", Updated: time.Now()})
	input := watching.HookInput{Session: owner.Session, Turn: owner.Turn, Event: "Stop"}
	if err := prepareHookRuntime(context.Background(), root, input, false); err != nil {
		t.Fatal(err)
	}
	if paused(root) {
		t.Fatal("idle worker cannot deliver completed review")
	}
	turn.Delivered = []string{"review"}
	_ = watching.Write(watching.TurnPath(root, owner.Session), turn)
	if completedReviewWaiting(root, input) {
		t.Fatal("already delivered review renews the worker")
	}
}

func TestNewestInstalledIgnoresUnverifiedVersions(t *testing.T) {
	home := t.TempDir()
	putRuntime(t, home, "2026.11.5.9")
	putRuntime(t, home, "2026.11.5.10")
	putRuntime(t, home, "2026.11.5.99")
	if err := os.WriteFile(runtimeversion.Binary(home, "2026.11.5.99"), []byte("modified"), 0700); err != nil {
		t.Fatal(err)
	}
	newest, err := (selfupdate.Updater{Home: home}).NewestInstalled("2026.11.5.9")
	if err != nil || newest != "2026.11.5.10" {
		t.Fatal(newest, err)
	}
}

func TestAdvanceLauncherAndRetryNeverDowngrade(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOOMER_CONFIG_DIR", t.TempDir())
	for _, release := range []string{"2026.11.5.9", "2026.11.6.1"} {
		putRuntime(t, home, release)
	}
	u := selfupdate.Updater{Home: home, Versioned: true}
	for _, release := range []string{"2026.11.5.9", "2026.11.6.1", "2026.11.5.9"} {
		result, err := advanceManagedLauncher(u, release)
		if err != nil {
			t.Fatal(err)
		}
		if release == "2026.11.6.1" && result.Version != release {
			t.Fatal(result)
		}
	}
	global := selfupdate.Updater{Home: home}
	result, err := global.Check("2026.11.6.1")
	if err != nil || result.State != "ready" {
		t.Fatal(result, err)
	}
	if result, err := u.InstallLauncher("2026.11.5.9", false); err != nil || result.Version != "2026.11.6.1" {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(runtimeversion.Binary(home, "2026.11.5.9")); err != nil {
		t.Fatal("old pinned binary was removed", err)
	}
}

func TestConcurrentLauncherInstallsNeverDowngrade(t *testing.T) {
	home := t.TempDir()
	for _, release := range []string{"2026.11.5.9", "2026.11.6.1"} {
		putRuntime(t, home, release)
	}
	u := selfupdate.Updater{Home: home, Versioned: true}
	var group sync.WaitGroup
	for _, release := range []string{"2026.11.5.9", "2026.11.6.1"} {
		group.Add(1)
		go func() {
			defer group.Done()
			// Contending installation locks are retryable. Neither ordering
			// may replace a newer published launcher with an older release.
			_, _ = u.InstallLauncher(release, false)
		}()
	}
	group.Wait()
	if _, err := u.InstallLauncher("2026.11.6.1", false); err != nil {
		t.Fatal(err)
	}
	if result, err := u.InstallLauncher("2026.11.5.9", false); err != nil || result.Version != "2026.11.6.1" {
		t.Fatal(result, err)
	}
}

func TestSideBySideUpgradeStartsCurrentSupervisorAndRetries(t *testing.T) {
	const release = "2026.11.6.1"
	home := t.TempDir()
	binary := runtimeversion.Binary(home, release)
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-ldflags", "-X github.com/doomerlabs/doomer/internal/version.Version="+release, "-o", binary, "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", output, err)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	record := selfupdate.Record{SchemaVersion: 1, Manager: "doomer-review", Path: binary, Version: release, SHA256: hex.EncodeToString(digest[:])}
	if err := watching.Write(filepath.Join(filepath.Dir(binary), "installation.json"), record); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("DOOMER_CONFIG_DIR", t.TempDir())
	api, profile := "https://example.test/api", "default"
	shared, config, err := consentPaths(api, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := watching.Write(config, watching.Config{API: api, Profile: profile}); err != nil {
		t.Fatal(err)
	}
	supervisor := filepath.Join(shared, "supervisor")
	t.Cleanup(func() {
		_ = watching.Write(filepath.Join(supervisor, "paused.json"), time.Now())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := waitWatch(ctx, func() (bool, error) { _, running, err := runningWatch(supervisor); return !running, err }); err != nil {
			t.Error(err)
		}
	})
	var firstSession string
	for range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		command := exec.CommandContext(ctx, binary, "self-update", "--side-by-side", "--version", release)
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("upgrade: %s %v", output, err)
		}
		var status selfupdate.Status
		if err := json.Unmarshal(output, &status); err != nil || status.LauncherVersion != release {
			t.Fatalf("upgrade result: %s %v", output, err)
		}
		process, running, err := runningWatch(supervisor)
		if err != nil || !running || process.Session != release {
			t.Fatal(process, running, err)
		}
		if firstSession != "" && process.ManagedSession != firstSession {
			t.Fatal("retry replaced a healthy supervisor")
		}
		firstSession = process.ManagedSession
	}
	installed, err := (selfupdate.Updater{Home: home}).Check(release)
	if err != nil || installed.State != "ready" {
		t.Fatal(installed, err)
	}
}
