package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestDirectoryWatchEnvironmentUsesNearestAncestor(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "test-apps")
	nested := filepath.Join(parent, "goblin", "deep", "directory")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".doomer.env"), []byte("DOOMER_API_URL=https://doomer.ai/api\nDOOMER_TOKEN=parent-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	text := "# Test routing\nDOOMER_API_URL=http://localhost:3000/api\nDOOMER_TOKEN=\nDOOMER_CONFIG_DIR=\"" + filepath.Join(root, "test credentials") + "\"\n"
	if err := os.WriteFile(filepath.Join(parent, ".doomer.env"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOOMER_API_URL", "https://doomer.ai/api")
	t.Setenv("DOOMER_TOKEN", "inherited-production-token")
	env, err := directoryWatchEnvironment(nested)
	if err != nil {
		t.Fatal(err)
	}
	if env.get("DOOMER_API_URL") != "http://localhost:3000/api" || env.get("DOOMER_TOKEN") != "" {
		t.Fatal("directory settings did not override inherited production settings")
	}
	if len(env) != 3 {
		t.Fatal("merged a farther ancestor's configuration")
	}
	api, err := agentHookAPI("https://explicit.example/api", env.get("DOOMER_API_URL"), "")
	if err != nil || api != "https://explicit.example/api" {
		t.Fatal("explicit endpoint did not win")
	}
}

func TestWatchEnvironmentSelectsCredentialsForEffectiveService(t *testing.T) {
	dir := t.TempDir()
	production, local := "https://doomer.ai/api", "http://localhost:3000/api"
	t.Setenv("DOOMER_TOKEN", "inherited-production-token")
	t.Setenv("DOOMER_CONFIG_DIR", filepath.Join(dir, "production"))
	t.Setenv("DOOMER_PROFILE", "")
	config := filepath.Join(dir, "testing")
	store := adversarylabs.ConfigStore{Path: filepath.Join(config, "config.json")}
	if err := store.SetAuth(adversarylabs.AuthKey(local, "default"), adversarylabs.Auth{Token: "local-account"}); err != nil {
		t.Fatal(err)
	}
	env := watchEnvironment{"DOOMER_API_URL": local, "DOOMER_TOKEN": "", "DOOMER_CONFIG_DIR": config}
	state := agentHookState{Identities: map[string]string{production: "production-anonymous", local: "local-anonymous"}}
	token, err := watchToken(NewRootCommand(), local, state, env)
	if err != nil || token != "local-account" {
		t.Fatalf("wrong credentials: %v", err)
	}
	if os.Getenv("DOOMER_TOKEN") != "inherited-production-token" {
		t.Fatal("watch changed the caller's environment")
	}
}

func TestWatchEnvironmentIsDataAndRejectsMalformedSettings(t *testing.T) {
	for _, input := range []string{"export DOOMER_API_URL=x", "UNKNOWN=x", "DOOMER_TOKEN=a\nDOOMER_TOKEN=b", "DOOMER_CONFIG_DIR=$HOME/config", "DOOMER_TOKEN=\"unfinished", "DOOMER_API_URL=", strings.Repeat("# comment\n", 8000)} {
		if _, err := parseWatchEnvironment(strings.NewReader(input)); err == nil {
			t.Fatalf("accepted invalid settings %q", input)
		}
	}
	env, err := parseWatchEnvironment(strings.NewReader("DOOMER_TOKEN='$(touch never-executed)'\n"))
	if err != nil || env["DOOMER_TOKEN"] != "$(touch never-executed)" {
		t.Fatal("values should be literal data")
	}
}
