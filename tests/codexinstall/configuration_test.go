package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentConfigurationKeepsCustomerSettingsOutOfFixture(t *testing.T) {
	source, home := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(source, "auth.json"), []byte(`{"fixture":"sign-in"}`), 0600)
	os.WriteFile(filepath.Join(source, "config.toml"), []byte("do not copy customer policy"), 0600)
	configText, err := configureAgent(config{Model: "test-model", CodexAuthHome: source}, home, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(configText, "do not copy") || strings.Contains(configText, "model_provider") {
		t.Fatal("inherited customer settings or proxy in subscription mode")
	}
	auth, err := os.ReadFile(filepath.Join(home, ".codex/auth.json"))
	if err != nil || string(auth) != `{"fixture":"sign-in"}` {
		t.Fatal("sign-in copy missing", err)
	}
	if _, err := configureAgent(config{CodexAuthHome: t.TempDir()}, t.TempDir(), ""); err == nil {
		t.Fatal("accepted missing subscription sign-in")
	}
	apiText, err := configureAgent(config{Model: "test-model"}, t.TempDir(), "http://fixture.invalid")
	if err != nil || !strings.Contains(apiText, `base_url = "http://fixture.invalid/v1"`) {
		t.Fatal("API fixture not isolated", apiText, err)
	}
	claudeHome := t.TempDir()
	if _, err := configureAgent(config{Harness: "claude"}, claudeHome, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(claudeHome, ".claude/.claude.json")); err != nil {
		t.Fatal(err)
	}
}

func TestAgentVersionMustMatchPinnedRuntime(t *testing.T) {
	for _, tc := range []struct {
		c       config
		version string
		want    bool
	}{
		{config{}, "codex-cli 0.167.0", true},
		{config{}, "codex-cli 0.168.0", false},
		{config{AgentVersion: "0.167.0-alpha.3.2"}, "codex-cli 0.167.0-alpha.3.2", true},
		{config{AgentVersion: "0.167.0-alpha.3.2"}, "codex-cli 0.167.0", false},
		{config{Harness: "claude"}, claudeVersion + " (Claude Code)", true},
		{config{Harness: "claude"}, "unknown", false},
	} {
		if got := validAgentVersion(tc.c, tc.version); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}
