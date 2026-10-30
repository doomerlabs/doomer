package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/application"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestAnonymousReviewIdentityIsPersistentIsolatedAndPrefersLogin(t *testing.T) {
	store := isolate(t)
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	get := func(api, profile string) adversarylabs.Auth {
		t.Helper()
		auth, err := reviewAuth(app, api, profile)
		if err != nil {
			t.Fatal(err)
		}
		return auth
	}
	api := "https://example.test/api"
	first := get(api, "local")
	if !adversarylabs.IsAnonymousToken(first.Token) {
		t.Fatal("anonymous identity missing")
	}
	if get(api, "local").Token != first.Token {
		t.Fatal("identity was not persisted")
	}
	if get(api, "other").Token == first.Token || get("https://other.test/api", "local").Token == first.Token {
		t.Fatal("identity crossed profile or service")
	}
	root, _, _ := watchPaths(api, "local")
	info, err := os.Stat(filepath.Join(root, "anonymous.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("anonymous capability must remain private")
	}
	if err = store.SetAuth(adversarylabs.AuthKey(api, "local"), adversarylabs.Auth{Token: "logged-in"}); err != nil {
		t.Fatal(err)
	}
	if get(api, "local").Token != "logged-in" {
		t.Fatal("anonymous identity replaced a login")
	}
}
func TestAnonymousWatchRequiresHumanUploadApproval(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	api, profile := "https://example.test/api", "guest"
	c := newWatchCommand(app, &api, &profile)
	c.SetContext(context.Background())
	c.SetIn(strings.NewReader("yes\n"))
	_ = c.Flags().Set("no-browser", "true")
	var output bytes.Buffer
	c.SetOut(&output)
	repo := newWatchTestRepo(t)
	_, _, err := prepareWatch(c, app, &api, &profile, []string{repo})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("anonymous upload was approved: %v", err)
	}
	_, file, _ := watchPaths(api, profile)
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("unapproved configuration saved")
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(watching.HookFile(home, "codex")); !os.IsNotExist(err) {
		t.Fatal("unapproved hooks installed")
	}
}
