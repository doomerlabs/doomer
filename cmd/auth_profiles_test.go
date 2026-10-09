package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/viper"
)

func TestRepeatLoginCreatesAndSelectsProfile(t *testing.T) {
	store := isolate(t)
	endpoint := "https://work.example/api"
	if _, err := execute(t, "first-token", "--api-url", endpoint, "login", "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "profiles", "add", "default-2", "--endpoint", endpoint); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAuth(adversarylabs.AuthKey(endpoint, "default-3"), adversarylabs.Auth{Token: "expired-token", ExpiresAt: "2020-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "second-token", "--api-url", endpoint, "login", "--token-stdin")
	if err != nil || !strings.Contains(out, "Created and selected profile default-4") || strings.Contains(out, "second-token") {
		t.Fatalf("repeat login: %q, %v", out, err)
	}
	for profile, token := range map[string]string{"default": "first-token", "default-3": "expired-token", "default-4": "second-token"} {
		auth, ok, err := store.StoredAuthE(adversarylabs.AuthKey(endpoint, profile))
		if err != nil || !ok || auth.Token != token {
			t.Fatalf("profile %s: %+v, %v, %v", profile, auth, ok, err)
		}
	}
	settings := viper.New()
	settings.SetConfigFile(filepath.Join(filepath.Dir(store.Path), "settings.yaml"))
	if err := settings.ReadInConfig(); err != nil {
		t.Fatal(err)
	}
	if settings.GetString("profile") != "default-4" || settings.GetString("profiles.default-4.api-url") != endpoint {
		t.Fatal("new profile selection or endpoint was not saved")
	}
	// The next command resolves the new profile and its endpoint from settings.
	if _, err := execute(t, "", "logout", "--local-only"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.StoredAuthE(adversarylabs.AuthKey(endpoint, "default-4")); err != nil || ok {
		t.Fatalf("selected profile was not logged out: %v, %v", ok, err)
	}
}

func TestLoginRefreshAndReplace(t *testing.T) {
	for _, tc := range []struct {
		name, expiry string
		replace      bool
	}{
		{name: "expired", expiry: "2020-01-01T00:00:00Z"},
		{name: "explicit replacement", replace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := isolate(t)
			key := adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "default")
			if err := store.SetAuth(key, adversarylabs.Auth{Token: "old-token", ExpiresAt: tc.expiry}); err != nil {
				t.Fatal(err)
			}
			args := []string{"login", "--token-stdin"}
			if tc.replace {
				args = append(args, "--replace")
			}
			out, err := execute(t, "new-token", args...)
			if err != nil || strings.Contains(out, "Created") {
				t.Fatalf("login: %q, %v", out, err)
			}
			auth, ok, err := store.ExactAuthE(key)
			if err != nil || !ok || auth.Token != "new-token" {
				t.Fatalf("refresh: %+v, %v, %v", auth, ok, err)
			}
		})
	}
}

func TestFailedRepeatLoginPreservesProfile(t *testing.T) {
	store := isolate(t)
	key := adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "default")
	if err := store.SetAuth(key, adversarylabs.Auth{Token: "old-token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "login", "--token-stdin"); err == nil {
		t.Fatal("expected empty token to fail")
	}
	if _, ok, err := store.StoredAuthE(adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "default-2")); err != nil || ok {
		t.Fatalf("failed login created credentials: %v, %v", ok, err)
	}
	auth, ok, err := store.ExactAuthE(key)
	if err != nil || !ok || auth.Token != "old-token" {
		t.Fatalf("old login changed: %+v, %v, %v", auth, ok, err)
	}
	out, err := execute(t, "", "profiles", "list")
	if err != nil || strings.Contains(out, "default-2") || !strings.Contains(out, "* default") {
		t.Fatalf("failed login changed settings: %q, %v", out, err)
	}
}
