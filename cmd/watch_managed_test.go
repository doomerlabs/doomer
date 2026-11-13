package cmd

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	watching "github.com/doomerlabs/doomer/internal/watch"
)

func TestManagedWatchPublishesStartupIdentityAndStops(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	repo := newWatchTestRepo(t)
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	api, profile := "http://127.0.0.1:1/api", "default"
	setup := newWatchCommand(app, &api, &profile)
	setup.SetContext(context.Background())
	setup.SetOut(io.Discard)
	if err := setup.Flags().Set("yes", "true"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareWatch(setup, app, &api, &profile, []string{repo}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := newManagedWatchCommand(app, &api, &profile)
	command.SetContext(ctx)
	command.SetOut(io.Discard)
	if err := command.Flags().Set("managed-session", "startup-test"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.RunE(command, nil) }()
	root, _, err := watchPaths(api, profile)
	if err != nil {
		t.Fatal(err)
	}
	for {
		var process watchProcess
		if watching.Read(filepath.Join(root, "process.json"), &process) == nil && process.ManagedSession == "startup-test" {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("exited before announcing readiness: %v", err)
		case <-ctx.Done():
			t.Fatal("managed watcher did not announce readiness")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, running, err := runningWatch(root); err != nil || running {
		t.Fatalf("watcher did not stop: %v %v", running, err)
	}
}
