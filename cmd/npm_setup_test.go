package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	watching "github.com/doomerlabs/doomer/internal/watch"
)

func TestNPMSetupLeavesExactGrantBytesInExistingState(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "state.json")
	exact := []byte("[\n\t{ \"scope\":\"global\", \"api\":\"https://other.example/api\", \"approvedAt\":\"2026-11-18T08:54:39.010Z\" }\n  ]")
	raw := append(append([]byte("{\"version\":1,\"grants\":"), exact...), '}')
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := readNPMSetupState(home)
	if err != nil {
		t.Fatal(err)
	}
	state.set("installer", "go")
	if err := writeNPMSetupState(home, state); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(after, exact) {
		t.Fatalf("grant bytes changed: %v", err)
	}
}

func TestNPMHookRepairPreservesHealthyHookPositions(t *testing.T) {
	file := filepath.Join(t.TempDir(), "hooks.json")
	o := npmSetupOptions{home: "/example/installer", executable: "/example/bin/doomer"}
	owner := "--owner=doomer-node"
	command, err := npmHookCommand(o, "codex", owner)
	if err != nil {
		t.Fatal(err)
	}
	if err = installNPMHookFile(file, "codex", owner, command); err != nil {
		t.Fatal(err)
	}
	config, _, err := npmReadSettings(file)
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := npmValidateHooks(config)
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := npmObjects(hooks["Stop"])
	hooks["Stop"] = append(groups, npmJSONObject{"hooks": []npmJSONObject{{"type": "command", "command": "other"}}})
	if err := watching.Write(file, config); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)
	if err := installNPMHookFile(file, "codex", owner, command); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("already healthy hook changed configuration byte content or positions")
	}
	oldRule := "Bash(/example/bin/doomer-2026.11.18.1 watch results --control *)"
	settings := npmJSONObject{"permissions": npmJSONObject{"allow": []any{"Read", oldRule}, "deny": []any{"Bash(rm *)"}}}
	rules, _, err := npmClaudeRules([]string{"/example/bin/doomer"})
	if err != nil {
		t.Fatal(err)
	}
	owned, changed, err := npmMergeClaudeAllow(settings, "permissions", nil, []string{oldRule}, rules)
	if err != nil || !changed || !slices.Equal(owned, rules) {
		t.Fatalf("scoped permission refresh failed: %v", err)
	}
	block := settings["permissions"].(npmJSONObject)
	allow := block["allow"].([]string)
	if slices.Contains(allow, oldRule) || !slices.Contains(allow, "Read") || len(block["deny"].([]any)) != 1 {
		t.Fatal("Claude refresh failed to preserve unrelated settings or remove obsolete owned rule")
	}
}

func TestNativeSetupInstallsClaudeBashPermissionDeniedHook(t *testing.T) {
	const owner, command = "--owner=doomer-node", "doomer watch agent --owner=doomer-node"
	file := filepath.Join(t.TempDir(), "settings.json")
	if err := installNPMHookFile(file, "claude", owner, command); err != nil {
		t.Fatal(err)
	}
	settings, _, err := npmReadSettings(file)
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := npmValidateHooks(settings)
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := npmObjects(hooks["PermissionDenied"])
	if len(groups) != 1 || groups[0]["matcher"] != "Bash" {
		t.Fatalf("expected only a Bash permission denial handler: %v", groups)
	}
	if mergeNPMHooks(hooks, "claude", owner, command) {
		t.Fatal("healthy setup rewrote the hooks")
	}
	mergeNPMHooks(hooks, "codex", owner, command)
	if _, exists := hooks["PermissionDenied"]; exists {
		t.Fatal("Claude-only hook remained after switching to Codex")
	}
}
