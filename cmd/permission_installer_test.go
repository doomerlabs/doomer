package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installerPermissionFixture(t *testing.T, grants string) (string, string) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("DOOMER_CONFIG_DIR", "")
	t.Setenv("DOOMER_HOME", "")
	t.Setenv("DOOMER_PROFILE", "")
	t.Setenv("CLAUDECODE", "")
	manager := filepath.Join(home, "data", "doomer-node")
	if err := os.MkdirAll(manager, 0700); err != nil {
		t.Fatal(err)
	}
	state := `{"version":1,"api":"http://localhost:3000/api","identities":{"http://localhost:3000/api":"private-token"},"grants":` + grants + `}`
	if err := os.WriteFile(filepath.Join(manager, "state.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	return manager, newWatchTestRepo(t)
}

func TestHasPermissionDiscoversInstallerApprovalReadOnly(t *testing.T) {
	manager, repo := installerPermissionFixture(t, `[{"scope":"global","api":"http://localhost:3000/api","approvedAt":"2026-11-12T12:00:00Z"}]`)
	before, _ := os.ReadFile(filepath.Join(manager, "state.json"))
	transport := http.DefaultTransport
	http.DefaultTransport = permissionNetworkGuard{t}
	t.Cleanup(func() { http.DefaultTransport = transport })
	for _, args := range [][]string{
		{"has-permission", repo, "--json"},
		{"has-permission", repo, "--json", "--agent", "claude"},
	} {
		output, err := execute(t, "", args...)
		var result struct {
			Approved bool
			API      string `json:"api_url"`
			Source   string
		}
		if err != nil || json.Unmarshal([]byte(output), &result) != nil || !result.Approved || result.API != "http://localhost:3000/api" || result.Source != "installer" {
			t.Fatalf("permission: %s %v", output, err)
		}
		if strings.Contains(output, "private-token") {
			t.Fatal("exposed credentials")
		}
	}
	after, _ := os.ReadFile(filepath.Join(manager, "state.json"))
	entries, err := os.ReadDir(manager)
	if err != nil || string(before) != string(after) || len(entries) != 1 {
		t.Fatal("permission check wrote installer state")
	}
	t.Setenv("DOOMER_API_URL", "https://doomer.ai/api")
	if output, err := execute(t, "", "has-permission", repo); err != nil || output != "false\n" {
		t.Fatalf("cross-service approval: %s %v", output, err)
	}
	if output, err := execute(t, "", "--api-url", "http://localhost:3000/api", "has-permission", repo); err != nil || output != "true\n" {
		t.Fatalf("explicit endpoint: %s %v", output, err)
	}
}

func TestHasPermissionInstallerScopeRevocationAndCorruption(t *testing.T) {
	manager, repo := installerPermissionFixture(t, `[]`)
	stateFile := filepath.Join(manager, "state.json")
	state := map[string]any{"version": 1, "api": "http://localhost:3000/api", "grants": []any{map[string]any{"repo": repo, "agents": []string{"codex"}, "api": "http://localhost:3000/api", "approvedAt": "2026-11-12T12:00:00Z"}}}
	write := func() {
		t.Helper()
		data, _ := json.Marshal(state)
		if err := os.WriteFile(stateFile, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	for _, tc := range []struct{ agent, path, want string }{{"codex", repo, "true\n"}, {"claude", repo, "false\n"}, {"codex", newWatchTestRepo(t), "false\n"}} {
		output, err := execute(t, "", "has-permission", tc.path, "--agent", tc.agent)
		if err != nil || output != tc.want {
			t.Fatalf("scope: %s %v", output, err)
		}
	}
	state["grants"] = []any{}
	write()
	if output, err := execute(t, "", "has-permission", repo); err != nil || output != "false\n" {
		t.Fatalf("revoked: %s %v", output, err)
	}
	if err := os.WriteFile(stateFile, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := execute(t, "", "has-permission", repo); err == nil || output != "" {
		t.Fatalf("corruption became approval: %s %v", output, err)
	}
}

func TestHasPermissionInstallerWorksWithDirectoryEnvironment(t *testing.T) {
	manager, repo := installerPermissionFixture(t, `[{"scope":"global","api":"http://localhost:3000/api","approvedAt":"2026-11-12T12:00:00Z"}]`)
	t.Setenv("DOOMER_CONFIG_DIR", t.TempDir())
	if output, err := execute(t, "", "has-permission", repo); err != nil || output != "true\n" {
		t.Fatalf("directory environment hid installer approval: %s %v", output, err)
	}
	if output, err := execute(t, "", "--profile", "default", "has-permission", repo); err != nil || output != "false\n" {
		t.Fatalf("explicit native profile was ignored: %s %v", output, err)
	}
	if output, err := execute(t, "", "has-permission", repo, "--manager-home", manager); err != nil || output != "true\n" {
		t.Fatalf("explicit installer config: %s %v", output, err)
	}
}
