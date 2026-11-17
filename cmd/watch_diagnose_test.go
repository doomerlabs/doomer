package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWatchDiagnoseExplainsDirectoryOverrideWithoutSecrets(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	home := filepath.Join(root, "manager")
	for _, dir := range []string{repo, home} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	state := `{"api":"https://dev.example/api","identities":{"https://dev.example/api":"SECRET_IDENTITY"},"grants":[{"scope":"global","api":"https://dev.example/api","approvedAt":"2026-11-16T00:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(home, "state.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".doomer.env"), []byte("DOOMER_API_URL=https://doomer.ai/api\nDOOMER_TOKEN=SECRET_TOKEN\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOOMER_API_URL", "https://dev.example/api")
	t.Setenv("DOOMER_CONFIG_DIR", filepath.Join(root, "credentials"))
	output, err := execute(t, "", "watch", "diagnose", repo, "--manager-home", home)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "SECRET_") {
		t.Fatal("diagnostic exposed a credential")
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result["service"] != "https://doomer.ai/api" || result["service_source"] != filepath.Join(root, ".doomer.env") || result["upload_approved"] != false || result["token_override_present"] != true {
		t.Fatal(output)
	}
	output, err = execute(t, "", "--api-url", "https://dev.example/api", "watch", "diagnose", repo, "--manager-home", home)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result["upload_approved"] != true || result["service_source"] != "--api-url" {
		t.Fatal(output)
	}
}
