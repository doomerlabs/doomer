package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestWatchTokenPrefersScopedAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOOMER_TOKEN", "")
	t.Setenv("DOOMER_PROFILE", "")
	t.Setenv("DOOMER_CONFIG_DIR", dir)
	api := "https://doomer.ai/api"
	store := adversarylabs.ConfigStore{Path: filepath.Join(dir, "config.json")}
	if err := store.SetAuth(adversarylabs.AuthKey(api, "default"), adversarylabs.Auth{Token: "account-token"}); err != nil {
		t.Fatal(err)
	}
	state := agentHookState{Identities: map[string]string{api: "anonymous-token", "http://localhost:3000/api": "local-anonymous"}, CredentialDirs: map[string]string{api: dir}}
	check := func(service, want string) {
		t.Helper()
		got, err := watchToken(NewRootCommand(), service, state, watchEnvironment{})
		if err != nil || got != want {
			t.Fatalf("credential resolution: got %q, err %v", got, err)
		}
	}
	check(api, "account-token")
	check("http://localhost:3000/api", "local-anonymous")
	t.Setenv("DOOMER_CONFIG_DIR", "")
	check(api, "account-token") // Onboard's service-specific directory survives missing inherited env.
	t.Setenv("DOOMER_TOKEN", "explicit-token")
	check(api, "explicit-token")
}

func TestWatchTokenHonorsProfileAndRejectsExpiredLogin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOOMER_TOKEN", "")
	t.Setenv("DOOMER_CONFIG_DIR", dir)
	t.Setenv("DOOMER_PROFILE", "work")
	api := "https://doomer.ai/api"
	store := adversarylabs.ConfigStore{Path: filepath.Join(dir, "config.json")}
	if err := store.SetAuth(adversarylabs.AuthKey(api, "work"), adversarylabs.Auth{Token: "work-account"}); err != nil {
		t.Fatal(err)
	}
	state := agentHookState{Identities: map[string]string{api: "anonymous-token"}}
	got, err := watchToken(NewRootCommand(), api, state, watchEnvironment{})
	if err != nil || got != "work-account" {
		t.Fatalf("wrong profile: %q, %v", got, err)
	}
	if err := store.SetAuth(adversarylabs.AuthKey(api, "work"), adversarylabs.Auth{Token: "expired", ExpiresAt: time.Now().Add(-time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if _, err := watchToken(NewRootCommand(), api, state, watchEnvironment{}); err == nil {
		t.Fatal("expired account silently became anonymous")
	}
}

func TestForegroundWatchRunsUntilCallerStopsIt(t *testing.T) {
	home, workspace := t.TempDir(), t.TempDir()
	api := "http://localhost:1/api"
	t.Setenv("DOOMER_HOME", home)
	t.Setenv("DOOMER_TOKEN", "test")
	t.Setenv("DOOMER_API_URL", "https://doomer.ai/api")
	t.Setenv("DOOMER_CONFIG_DIR", t.TempDir())
	if err := os.WriteFile(filepath.Join(workspace, ".doomer.env"), []byte("DOOMER_API_URL="+api+"\nDOOMER_TOKEN=\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"version": 1, "api": "https://doomer.ai/api", "identities": map[string]string{api: "local-token"}, "grants": []any{map[string]any{"scope": "global", "api": api, "approvedAt": time.Now().UTC()}}}
	data, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(home, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := NewRootCommand()
	c.SetArgs([]string{"watch", "--", workspace})
	c.SetContext(ctx)
	done := make(chan error, 1)
	go func() { done <- c.Execute() }()
	select {
	case err := <-done:
		t.Fatalf("watch exited before caller stopped it: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not stop")
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 1 {
		t.Fatalf("watch wrote runtime files: %v %v", entries, err)
	}
}
