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
func TestAnonymousWatchInvocationInstallsWithoutExtraUploadPrompt(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	api, profile := "https://example.test/api", "guest"
	c := newWatchCommand(app, &api, &profile)
	c.SetContext(context.Background())
	c.SetIn(strings.NewReader(""))
	var output bytes.Buffer
	c.SetOut(&output)
	cfg, auth, err := prepareWatch(c, app, &api, &profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !adversarylabs.IsAnonymousToken(auth.Token) || cfg.AccountKey != watching.Key(auth.Token) || !cfg.AllRepositories {
		t.Fatal("anonymous watch configuration missing")
	}
	if !strings.Contains(output.String(), "Source code") || strings.Contains(output.String(), auth.Token) {
		t.Fatal("disclosure or secret handling incorrect")
	}
	home, _ := os.UserHomeDir()
	if _, err = os.Stat(watching.HookFile(home, "codex")); err != nil {
		t.Fatal(err)
	}
}
