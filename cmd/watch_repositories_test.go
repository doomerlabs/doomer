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
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestWatchOwnsIntegrationCommandsWithoutTopLevelSetup(t *testing.T) {
	isolate(t)
	root := NewRootCommand()
	for _, command := range root.Commands() {
		if command.Name() == "setup" {
			t.Fatal("obsolete top-level setup command is still registered")
		}
	}
	for _, name := range []string{"ls", "add", "rm", "uninstall"} {
		command, _, err := root.Find([]string{"watch", name})
		if err != nil || command.Name() != name {
			t.Fatalf("missing watch %s: %v", name, err)
		}
	}
}

func TestWatchFirstUseRequiresUploadApproval(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	api, profile := "https://example.com/api", "local"
	if err := store.SetAuth(adversarylabs.AuthKey(api, profile), adversarylabs.Auth{Token: "token"}); err != nil {
		t.Fatal(err)
	}
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	c := newWatchCommand(app, &api, &profile)
	c.SetContext(context.Background())
	c.SetIn(strings.NewReader("yes\n"))
	_ = c.Flags().Set("no-browser", "true")
	var output bytes.Buffer
	c.SetOut(&output)
	_, _, err := prepareWatch(c, app, &api, &profile, []string{newWatchTestRepo(t)})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("first use bypassed approval: %v", err)
	}
	_, file, _ := watchPaths(api, profile)
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("unapproved setup persisted configuration")
	}
}

func TestWatchAddListRemovePreservesApprovalAndProfileIsolation(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	api, profile := "https://example.com/api", "local"
	if err := store.SetAuth(adversarylabs.AuthKey(api, profile), adversarylabs.Auth{Token: "token"}); err != nil {
		t.Fatal(err)
	}
	root, file, _ := watchPaths(api, profile)
	cfg := watching.Config{Version: 1, API: api, Profile: profile, AllRepositories: true, AccountKey: watching.Key("token"), ConsentedAt: time.Now()}
	if err := watching.Write(file, cfg); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	marker := "watch hook --state " + watching.Quote(root)
	if err := watching.Install(watching.HookFile(home, "codex"), "doomer "+marker, marker, false); err != nil {
		t.Fatal(err)
	}
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	c := newWatchCommand(app, &api, &profile)
	c.SetContext(context.Background())
	var output bytes.Buffer
	c.SetOut(&output)
	list, _, err := c.Find([]string{"ls"})
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir()
	if err := watching.RegisterRepository(root, path, "explicit", true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	list.SetContext(context.Background())
	if err := list.RunE(list, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), filepath.Base(path)) || strings.Contains(output.String(), path) || !strings.Contains(output.String(), "Not running") {
		t.Fatal(output.String())
	}
	remove, _, err := c.Find([]string{"rm"})
	if err != nil {
		t.Fatal(err)
	}
	remove.SetContext(context.Background())
	if err := remove.RunE(remove, []string{path}); err != nil {
		t.Fatal(err)
	}
	if !watching.RepositoryRemoved(root, path) {
		t.Fatal("removal was not saved")
	}
	other, _, _ := watchPaths(api, "production")
	if watching.RepositoryRemoved(other, path) {
		t.Fatal("removal escaped selected profile")
	}
	var after watching.Config
	if err := watching.Read(file, &after); err != nil || !after.ConsentedAt.Equal(cfg.ConsentedAt) {
		t.Fatal("selection edit changed upload approval")
	}
}
