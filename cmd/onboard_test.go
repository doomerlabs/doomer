package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/publock"
	watching "github.com/doomerlabs/doomer/internal/watch"
)

func TestOnboardReusesConsentAndWatcherWithoutLosingInstructions(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	api, profile := "http://localhost:3000/api", "onboard-test"
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	repo := newWatchTestRepo(t)
	setup := newWatchCommand(app, &api, &profile)
	setup.SetContext(context.Background())
	setup.SetOut(&bytes.Buffer{})
	setup.Flags().Set("yes", "true")
	cfg, _, err := prepareWatch(setup, app, &api, &profile, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	root, _, _ := watchPaths(api, profile)
	lock, err := publock.TryAcquire(root, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	watching.Write(filepath.Join(root, "process.json"), watchProcess{PID: os.Getpid(), ConsentedAt: cfg.ConsentedAt})
	watching.Write(filepath.Join(root, "heartbeat.json"), time.Now())
	os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("Keep existing rules.\n"), 0600)
	// Any unexpected browser reapproval fails this test.
	previous := openSetupBrowser
	defer func() { openSetupBrowser = previous }()
	openSetupBrowser = func(context.Context, string) error { t.Fatal("repeated consent"); return nil }
	c := newOnboardCommand(app, &api, &profile)
	c.SetContext(context.Background())
	var output bytes.Buffer
	c.SetOut(&output)
	c.ParseFlags([]string{"--repo", repo, "--session", "session", "--task-stdin"})
	c.SetIn(strings.NewReader("Add pagination"))
	if err := c.RunE(c, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Reusing the running watcher") || strings.Contains(output.String(), "One restart left") {
		t.Fatal(output.String())
	}
	instructions, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if !strings.HasPrefix(string(instructions), "Keep existing rules.") {
		t.Fatal(string(instructions))
	}
	// The explicit restart path saves task context in the same onboarding call.
	c.Flags().Set("restart", "true")
	c.SetIn(strings.NewReader("Add pagination"))
	output.Reset()
	if err := c.RunE(c, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "codex resume 'session' 'Resume after doomer.ai setup'") {
		t.Fatal(output.String())
	}
	if original, err := watching.ResumeTaskContext(root, repo, "session"); err != nil || original != "Add pagination" {
		t.Fatalf("lost restart task: %q %v", original, err)
	}
	c.SetIn(strings.NewReader("Add pagination"))
	watching.Write(filepath.Join(root, "paused.json"), true)
	if err := c.RunE(c, nil); err == nil {
		t.Fatal("onboarding resumed a paused watcher")
	}
}
