package cmd

import (
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestProfileRenameAndRemove(t *testing.T) {
	store := isolate(t)
	endpoint := "http://localhost:3000/api"
	for _, args := range [][]string{
		{"profile", "add", "work", "--endpoint", endpoint},
		{"profile", "use", "work"},
	} {
		if _, err := execute(t, "", args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, api := range []string{endpoint, adversarylabs.DefaultAPIURL} {
		if err := store.SetAuth(adversarylabs.AuthKey(api, "work"), adversarylabs.Auth{Token: "work-token", ExpiresAt: "2020-01-01T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetAuth(adversarylabs.AuthKey(endpoint, "personal"), adversarylabs.Auth{Token: "personal-token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "profile", "rename", "WORK", "Company"); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "", "profile", "ls")
	if err != nil || !strings.Contains(out, "* company\t"+endpoint) || strings.Contains(out, "work") {
		t.Fatalf("renamed profile listing: %q, %v", out, err)
	}
	for _, api := range []string{endpoint, adversarylabs.DefaultAPIURL} {
		if _, ok, err := store.StoredAuthE(adversarylabs.AuthKey(api, "work")); err != nil || ok {
			t.Fatalf("old credentials remain: %v, %v", ok, err)
		}
		auth, ok, err := store.StoredAuthE(adversarylabs.AuthKey(api, "company"))
		if err != nil || !ok || auth.Token != "work-token" || auth.ExpiresAt != "2020-01-01T00:00:00Z" {
			t.Fatalf("credentials not preserved: %+v, %v, %v", auth, ok, err)
		}
	}
	if _, err := execute(t, "", "profile", "rm", "company"); err != nil {
		t.Fatal(err)
	}
	out, err = execute(t, "", "profile", "ls")
	if err != nil || !strings.Contains(out, "* default") || strings.Contains(out, "company") {
		t.Fatalf("removed profile listing: %q, %v", out, err)
	}
	for _, api := range []string{endpoint, adversarylabs.DefaultAPIURL} {
		if _, ok, err := store.StoredAuthE(adversarylabs.AuthKey(api, "company")); err != nil || ok {
			t.Fatalf("removed credentials remain: %v, %v", ok, err)
		}
	}
	if auth, ok, err := store.StoredAuthE(adversarylabs.AuthKey(endpoint, "personal")); err != nil || !ok || auth.Token != "personal-token" {
		t.Fatalf("unrelated credentials changed: %+v, %v, %v", auth, ok, err)
	}
}

func TestProfileManagementRejectsCollisionsAndInvalidNames(t *testing.T) {
	store := isolate(t)
	for _, name := range []string{"work", "company"} {
		if _, err := execute(t, "", "profile", "add", name); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetAuth(adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "credentials-only"), adversarylabs.Auth{Token: "keep-token"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"rename", "work", "company"},
		{"rename", "work", "credentials-only"},
		{"rename", "work", "default"},
		{"rename", "work", "work"},
		{"rename", "missing", "new"},
		{"rename", "work", "../bad"},
		{"rm", "missing"},
		{"rm", "../bad"},
	} {
		if _, err := execute(t, "", append([]string{"profile"}, args...)...); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
	if auth, ok, err := store.StoredAuthE(adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "credentials-only")); err != nil || !ok || auth.Token != "keep-token" {
		t.Fatal("collision damaged credentials")
	}
}

func TestDefaultProfileRenameMigratesLegacyLogin(t *testing.T) {
	store := isolate(t)
	if err := store.SetAuth(adversarylabs.DefaultRegistry, adversarylabs.Auth{Token: "legacy-token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "profile", "rename", "default", "personal"); err != nil {
		t.Fatal(err)
	}
	if auth, ok, err := store.StoredAuthE(adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "personal")); err != nil || !ok || auth.Token != "legacy-token" {
		t.Fatalf("legacy login not moved: %+v, %v, %v", auth, ok, err)
	}
	if _, ok, err := store.StoredAuthE(adversarylabs.DefaultRegistry); err != nil || ok {
		t.Fatal("legacy login remains")
	}
	out, err := execute(t, "", "profile", "ls")
	if err != nil || !strings.Contains(out, "* personal") {
		t.Fatalf("default selection not renamed: %q, %v", out, err)
	}
}
