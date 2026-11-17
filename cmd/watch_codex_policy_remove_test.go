package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveNodeCodexPoliciesPreservesOtherScopesAndCustomText(t *testing.T) {
	own := managedConsentEvidence{CredentialDirectory: "/prod", Account: "user", Service: "https://doomer.ai/api", Profile: "default", Agent: "codex", AllRepositories: true}
	other := own
	other.CredentialDirectory = "/dev"
	local := own
	local.Service = "http://localhost:3000/api"
	edited := strings.Replace(proposedCodexReviewPolicy(own).ExtraPolicy, "The user approved", "Custom approval", 1)
	state := agentHookState{CredentialDirs: map[string]string{own.Service: "/prod"}}
	for _, explicit := range []bool{true, false} {
		preserved := "Keep this policy\n" + proposedCodexReviewPolicy(other).ExtraPolicy + proposedCodexReviewPolicy(local).ExtraPolicy + edited
		input := codexReviewPolicyText(own, explicit).ExtraPolicy + preserved
		if got := stripNodeCodexPolicies(input, state, nil); got != preserved {
			t.Fatal("removed unrelated or edited policy")
		}
		if got := stripNodeCodexPolicies(input, state, []string{own.Service}); got != input {
			t.Fatal("removed shared scope")
		}
	}
}

func TestRemoveNodeCodexPolicyOfflineAndIdempotent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	own := managedConsentEvidence{CredentialDirectory: "/prod", Account: "user", Service: "https://doomer.ai/api", Profile: "default", Agent: "codex", AllRepositories: true}
	data, err := patchCodexExtraPolicy([]byte("# retain\nmodel='test'\n"), "Unrelated rule\n"+proposedCodexReviewPolicy(own).ExtraPolicy)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, "config.toml")
	if err = os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager := t.TempDir()
	state := `{"version":1,"credentialDirs":{"https://doomer.ai/api":"/prod"},"grants":[]}`
	if err = os.WriteFile(filepath.Join(manager, "state.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err := execute(t, "", "watch", "codex-policy", "--remove", "--manager-home", manager)
		if err != nil || !strings.Contains(out, `"removed":true`) {
			t.Fatalf("%s %v", out, err)
		}
	}
	got, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := codexExtraPolicy(got)
	if err != nil || policy != "Unrelated rule\n" {
		t.Fatalf("%q %v", policy, err)
	}
	if !strings.Contains(string(got), "# retain\nmodel='test'") {
		t.Fatal("rewrote unrelated settings")
	}
}

func TestRemoveNodeCodexPolicyUsesRecordedOwnershipWithoutCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	consent := managedConsentEvidence{CredentialDirectory: "/external", Account: "user", Service: "https://doomer.ai/api", Profile: "default", Agent: "codex", AllRepositories: true}
	block := proposedCodexReviewPolicy(consent).ExtraPolicy
	data, err := patchCodexExtraPolicy(nil, block+"Keep unrelated text")
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, "config.toml")
	if err = os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	manager := t.TempDir()
	state, _ := json.Marshal(map[string]any{"version": 1, "codexPolicies": []map[string]string{{"configPath": config, "extraPolicy": block}}})
	if err = os.WriteFile(filepath.Join(manager, "state.json"), state, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := execute(t, "", "watch", "codex-policy", "--remove", "--manager-home", manager); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	got, _ := os.ReadFile(config)
	policy, err := codexExtraPolicy(got)
	if err != nil || policy != "Keep unrelated text" {
		t.Fatalf("%q %v", policy, err)
	}
}
