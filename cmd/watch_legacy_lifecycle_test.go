package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	watching "github.com/doomerlabs/doomer/internal/watch"
)

// Exercise a real detached CLI process using the already compiled test executable.
func TestLegacyOnboardingSubprocessLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix detached process")
	}
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	api, profile := fakeSetupAPI(t), "legacy-lifecycle"
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	repo, other := newWatchTestRepo(t), newWatchTestRepo(t)
	wrapper := hookTestExecutable(t)
	previous := managedWatchExecutable
	managedWatchExecutable = func() (string, error) { return wrapper, nil }
	t.Cleanup(func() { managedWatchExecutable = previous })
	root, file, err := watchPaths(api, profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = watching.Write(filepath.Join(sharedWatchRoot(root), "paused.json"), time.Now())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = waitWatch(ctx, func() (bool, error) { _, live, err := runningWatch(root); return !live, err })
	})
	start := func(path string) watchProcess {
		t.Helper()
		c := newOnboardingWatchCommand(app, &api, &profile)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		c.SetContext(ctx)
		c.SetOut(io.Discard)
		c.SetErr(io.Discard)
		if err := c.Flags().Set("yes", "true"); err != nil {
			t.Fatal(err)
		}
		if err := c.RunE(c, []string{path}); err != nil {
			t.Fatal(err)
		}
		process, live, err := runningWatch(root)
		if err != nil || !live || process.PID == os.Getpid() {
			t.Fatalf("missing detached watcher: %+v %v", process, err)
		}
		return process
	}
	first := start(repo)
	if reused := start(repo); reused.PID != first.PID {
		t.Fatal("repeated onboarding spawned another watcher")
	}
	replaced := start(other)
	if replaced.PID == first.PID {
		t.Fatal("changed consent did not replace the old watcher")
	}
	command := newWatchCommand(app, &api, &profile)
	revoke, _, err := command.Find([]string{"revoke"})
	if err != nil {
		t.Fatal(err)
	}
	revoke.SetContext(context.Background())
	revoke.SetOut(io.Discard)
	if err := revoke.RunE(revoke, []string{repo}); err != nil {
		t.Fatal(err)
	}
	if _, live, err := runningWatch(root); err != nil || live {
		t.Fatalf("revocation did not stop watcher: %v %v", live, err)
	}
	var cfg watching.Config
	if err := watching.Read(file, &cfg); err != nil {
		t.Fatal(err)
	}
	if watching.Allowed(cfg, repo) || !watching.Allowed(cfg, other) {
		t.Fatal("revocation affected the wrong repository")
	}
}
