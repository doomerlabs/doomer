package adversarylabs

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCredentialCASAndCanonicalScope(t *testing.T) {
	store := ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
	key := AuthKey("HTTPS://API.Example:443/TenantA/", "Work")
	if key != AuthKey("https://api.example/TenantA", "work") {
		t.Fatal("service canonicalization failed")
	}
	if key == AuthKey("https://api.example/tenanta", "work") || key == AuthKey("https://api.example/TenantA", "personal") {
		t.Fatal("credential scopes collided")
	}
	first := Auth{Token: "first"}
	second := Auth{Token: "second"}
	if err := store.SetAuth(key, first); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAuth(key, second); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAuthCAS(key, first); !errors.Is(err, ErrAuthCAS) {
		t.Fatalf("CAS: %v", err)
	}
	auth, ok, err := store.ExactAuthE(key)
	if err != nil || !ok || auth.Token != "second" {
		t.Fatal("concurrent login lost")
	}
}

func TestConcurrentLoginCannotOverwriteReservedCredentials(t *testing.T) {
	store := ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
	key := AuthKey(DefaultAPIURL, "default-2")
	results := make(chan error, 2)
	for _, token := range []string{"first", "second"} {
		go func(value string) { results <- store.SetAuthCAS(key, Auth{}, false, Auth{Token: value}) }(token)
	}
	success, conflict := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrAuthCAS) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("expected one login and one conflict, got %d and %d", success, conflict)
	}
}

func TestDefaultProjectFollowsProfileLifecycle(t *testing.T) {
	store := ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
	oldKey := AuthKey(DefaultAPIURL, "work")
	newKey := AuthKey(DefaultAPIURL, "renamed")
	if err := store.SetAuth(oldKey, Auth{Token: "before"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultProject(oldKey, "team-id"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAuth(oldKey, Auth{Token: "refreshed"}); err != nil {
		t.Fatal(err)
	}
	if value, err := store.DefaultProject(oldKey); err != nil || value != "team-id" {
		t.Fatalf("refresh lost selection: %q %v", value, err)
	}
	if err := store.RenameProfile("work", "renamed", DefaultRegistry); err != nil {
		t.Fatal(err)
	}
	if value, err := store.DefaultProject(newKey); err != nil || value != "team-id" {
		t.Fatalf("rename lost selection: %q %v", value, err)
	}
	if value, err := store.DefaultProject(oldKey); err != nil || value != "" {
		t.Fatalf("old selection remains: %q %v", value, err)
	}
	if err := store.RemoveProfile("renamed", DefaultRegistry); err != nil {
		t.Fatal(err)
	}
	if value, err := store.DefaultProject(newKey); err != nil || value != "" {
		t.Fatalf("removed selection remains: %q %v", value, err)
	}
}

func TestLogoutClearsDefaultProject(t *testing.T) {
	for _, method := range []string{"cas", "direct", "legacy"} {
		t.Run(method, func(t *testing.T) {
			store := ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
			key := AuthKey(DefaultAPIURL, "work")
			selectionKey := key
			if method == "legacy" {
				key = DefaultRegistry
				selectionKey = AuthKey(DefaultAPIURL, "default")
			}
			auth := Auth{Token: "token"}
			if err := store.SetAuth(key, auth); err != nil {
				t.Fatal(err)
			}
			if err := store.SetDefaultProject(selectionKey, "old-account-project"); err != nil {
				t.Fatal(err)
			}
			other := AuthKey("https://other.example/api", "work")
			if err := store.SetDefaultProject(other, "unrelated-project"); err != nil {
				t.Fatal(err)
			}
			if method == "direct" {
				if _, _, err := store.RemoveAuth(key); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := store.RemoveAuthCAS(key, auth); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := store.DefaultProject(selectionKey); err != nil || got != "" {
				t.Fatalf("orphaned project: %q %v", got, err)
			}
			if got, err := store.DefaultProject(other); err != nil || got != "unrelated-project" {
				t.Fatalf("other scope affected: %q %v", got, err)
			}
			if err := store.SetAuth(key, Auth{Token: "new-account"}); err != nil {
				t.Fatal(err)
			}
			if got, err := store.DefaultProject(selectionKey); err != nil || got != "" {
				t.Fatalf("new account inherited project: %q %v", got, err)
			}
		})
	}
}

func TestFailedLogoutPreservesDefaultProject(t *testing.T) {
	store := ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
	key := AuthKey(DefaultAPIURL, "work")
	if err := store.SetAuth(key, Auth{Token: "new-login"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultProject(key, "current-project"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAuthCAS(key, Auth{Token: "old-login"}); !errors.Is(err, ErrAuthCAS) {
		t.Fatalf("expected conflict: %v", err)
	}
	if got, err := store.DefaultProject(key); err != nil || got != "current-project" {
		t.Fatalf("concurrent selection lost: %q %v", got, err)
	}
}

func TestLoggedOutProfileNameCanBeReused(t *testing.T) {
	store := ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
	work := AuthKey(DefaultAPIURL, "work")
	auth := Auth{Token: "old-account"}
	if err := store.SetAuth(work, auth); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultProject(work, "old-project"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAuthCAS(work, auth); err != nil {
		t.Fatal(err)
	}
	personal := AuthKey(DefaultAPIURL, "personal")
	if err := store.SetAuth(personal, Auth{Token: "personal"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDefaultProject(personal, "personal-project"); err != nil {
		t.Fatal(err)
	}
	if err := store.RenameProfile("personal", "work", DefaultRegistry); err != nil {
		t.Fatal(err)
	}
	if got, err := store.DefaultProject(work); err != nil || got != "personal-project" {
		t.Fatalf("renamed project: %q %v", got, err)
	}
}
