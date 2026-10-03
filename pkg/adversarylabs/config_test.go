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
