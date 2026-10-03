package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func execute(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	root := NewRootCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetIn(strings.NewReader(input))
	root.SetArgs(args)
	err := root.Execute()
	return output.String(), err
}
func isolate(t *testing.T) adversarylabs.ConfigStore {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DOOMER_CONFIG_DIR", dir)
	for _, key := range []string{"DOOMER_PROFILE", "DOOMER_API_URL", "DOOMER_REGISTRY_HOST", "ADVERSARY_API_URL", "ADVERSARY_REGISTRY_HOST"} {
		t.Setenv(key, "")
	}
	return adversarylabs.ConfigStore{Path: filepath.Join(dir, "config.json")}
}
func TestProfilesAndCredentialIsolation(t *testing.T) {
	store := isolate(t)
	for _, args := range [][]string{
		{"profiles", "add", "work", "--endpoint", "https://work.example/api"},
		{"profiles", "use", "work"},
	} {
		if _, err := execute(t, "", args...); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := execute(t, "work-token\n", "login", "--token-stdin"); err != nil || strings.Contains(out, "work-token") {
		t.Fatalf("login: %q %v", out, err)
	}
	if _, err := execute(t, "personal-token\n", "--profile", "default", "login", "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "logout", "--local-only"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ExactAuthE(adversarylabs.AuthKey("https://work.example/api", "work")); err != nil || ok {
		t.Fatalf("work logout: %v %v", ok, err)
	}
	if auth, ok, err := store.ExactAuthE(adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "default")); err != nil || !ok || auth.Token != "personal-token" {
		t.Fatalf("default profile damaged: %v %v", ok, err)
	}
	info, err := os.Stat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("credential permissions: %v", info.Mode())
	}
	if out, err := execute(t, "", "profiles", "list"); err != nil || strings.Contains(out, "personal-token") || !strings.Contains(out, "* work") {
		t.Fatalf("list: %q %v", out, err)
	}
}
func TestPasswordLoginAndRevocationFailurePreservesCredentials(t *testing.T) {
	store := isolate(t)
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/login":
			var input adversarylabs.PasswordLoginOptions
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.EmailAddress != "user@example.com" || input.Password != "secret" || input.Team != "" {
				t.Errorf("wrong password payload: email=%q team=%q", input.EmailAddress, input.Team)
			}
			_, _ = w.Write([]byte(`{"token":"test-token","client_id":"client"}`))
		case "/v1/auth/revoke":
			if r.Header.Get("Authorization") != "Bearer test-token" {
				t.Error("missing scoped token")
			}
			if fail {
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"message":"test-token secret"}`))
				return
			}
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	args := []string{"--api-url", server.URL, "--profile", "work"}
	if out, err := execute(t, "secret\n", append(args, "login", "--email-address", "user@example.com", "--password-stdin")...); err != nil || strings.Contains(out, "test-token") {
		t.Fatalf("password login: %q %v", out, err)
	}
	if _, err := execute(t, "", append(args, "logout")...); err == nil || strings.Contains(err.Error(), "test-token") {
		t.Fatalf("revocation failure: %v", err)
	}
	key := adversarylabs.AuthKey(server.URL, "work")
	if _, ok, err := store.ExactAuthE(key); err != nil || !ok {
		t.Fatalf("credentials not preserved: %v %v", ok, err)
	}
	fail = false
	if _, err := execute(t, "", append(args, "logout")...); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ExactAuthE(key); err != nil || ok {
		t.Fatalf("credentials not removed: %v %v", ok, err)
	}
}
func TestEnvironmentAndFlagPrecedence(t *testing.T) {
	store := isolate(t)
	t.Setenv("DOOMER_PROFILE", "environment")
	t.Setenv("DOOMER_API_URL", "https://env.example/api")
	if _, err := execute(t, "env-token", "login", "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "flag-token", "--profile", "flag", "--api-url", "https://flag.example/api", "login", "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ url, profile, token string }{{"https://env.example/api", "environment", "env-token"}, {"https://flag.example/api", "flag", "flag-token"}} {
		auth, ok, err := store.ExactAuthE(adversarylabs.AuthKey(entry.url, entry.profile))
		if err != nil || !ok || auth.Token != entry.token {
			t.Fatalf("wrong credential scope: %s %v", entry.profile, err)
		}
	}
}
func TestRejectsUnsafeEndpointsAndInvalidModes(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{
		{"--api-url", "http://remote.example/api", "login", "--token-stdin"},
		{"--profile", "../outside", "login", "--token-stdin"},
		{"login", "--device", "--password-stdin", "--email-address", "user@example.com"},
		{"login", "--token-stdin", "--ci"},
		{"login", "--token-stdin"},
		{"run"},
	} {
		if _, err := execute(t, "", args...); err == nil {
			t.Fatalf("accepted invalid command: %v", args)
		}
	}
}

func TestExpiredCredentialsCanBeRemoved(t *testing.T) {
	store := isolate(t)
	key := adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "default")
	if err := store.SetAuth(key, adversarylabs.Auth{Token: "expired", ExpiresAt: "2020-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "logout", "--local-only"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.StoredAuthE(key); err != nil || ok {
		t.Fatalf("expired token remains: %v %v", ok, err)
	}
}
func TestDeviceLogin(t *testing.T) {
	store := isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/device/code":
			_, _ = w.Write([]byte(`{"device_code":"device","user_code":"ABCD","verification_uri":"https://doomer.ai/device","expires_in":60,"interval":1}`))
		case "/v1/auth/device/token":
			_, _ = w.Write([]byte(`{"token":"device-token"}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	out, err := execute(t, "", "--api-url", server.URL, "login", "--device")
	if err != nil || !strings.Contains(out, "ABCD") || strings.Contains(out, "device-token") {
		t.Fatalf("device: %q %v", out, err)
	}
	if auth, ok, err := store.ExactAuthE(adversarylabs.AuthKey(server.URL, "default")); err != nil || !ok || auth.Token != "device-token" {
		t.Fatal("device token not saved")
	}
}

func TestLoginIgnoresLegacyEnvironmentAndStoresDoomerRegistry(t *testing.T) {
	store := isolate(t)
	t.Setenv("ADVERSARY_API_URL", "http://localhost:3000/api")
	t.Setenv("ADVERSARY_REGISTRY_HOST", "localhost:9999")
	t.Setenv("ADVERSARY_MODEL", "unused-model")
	t.Setenv("ADVERSARY_MODEL_PROVIDER", "unused-provider")
	t.Setenv("DOOMER_REGISTRY_HOST", " localhost:8787 ")
	if _, err := execute(t, "test-token", "login", "--token-stdin"); err != nil {
		t.Fatal(err)
	}
	auth, ok, err := store.ExactAuthE(adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "default"))
	if err != nil || !ok || auth.RegistryHost != "localhost:8787" {
		t.Fatalf("wrong endpoint or registry: found=%v registry=%q error=%v", ok, auth.RegistryHost, err)
	}
}

func TestAccountProfilesKeepPersonalLoginsSeparate(t *testing.T) {
	store := isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if _, exists := payload["team"]; exists {
			t.Error("login should not select a project")
		}
		if r.URL.Path != "/v1/auth/login" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "token-" + payload["email_address"].(string), "client_id": "account"})
	}))
	defer server.Close()
	for _, name := range []string{"work", "personal"} {
		if _, err := execute(t, "", "profiles", "add", name, "--endpoint", server.URL); err != nil {
			t.Fatal(err)
		}
		if _, err := execute(t, "secret", "--profile", name, "login", "--email-address", name+"@example.com", "--password-stdin"); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"work", "personal"} {
		auth, ok, err := store.ExactAuthE(adversarylabs.AuthKey(server.URL, name))
		if err != nil || !ok || auth.Token != "token-"+name+"@example.com" {
			t.Fatalf("wrong account for profile %s: %v", name, err)
		}
	}
	if _, err := execute(t, "", "--profile", "work", "logout", "--local-only"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.ExactAuthE(adversarylabs.AuthKey(server.URL, "work")); err != nil || ok {
		t.Fatalf("work login not removed: %v", err)
	}
	if auth, ok, err := store.ExactAuthE(adversarylabs.AuthKey(server.URL, "personal")); err != nil || !ok || auth.Token != "token-personal@example.com" {
		t.Fatalf("other account was affected: %v", err)
	}
	if _, ok, err := store.ExactAuthE(adversarylabs.AuthKey(server.URL, "default")); err != nil || ok {
		t.Fatalf("account leaked into default profile: %v", err)
	}
}
