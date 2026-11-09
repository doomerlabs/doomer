package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexTemplateRepairPreservesDefaultAndRefreshesManagedTemplate(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "config.toml")
	writeCatalog := func(template string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"models": []any{map[string]any{"slug": "codex-auto-review", "model_messages": map[string]any{"auto_review": map[string]any{"policy_template": template}}}}})
		if err := os.WriteFile(filepath.Join(home, "models_cache.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	original := "Preserve all safety instructions.\n{{ tenant_policy_config }}\nEnd original template."
	writeCatalog(original)
	consent := managedConsentEvidence{Account: "account", Service: "https://doomer.ai/api", Profile: "default", Agent: "codex", AllRepositories: true}
	if err := installCodexReviewPolicy(consent); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	template, err := codexAutoReviewString(data, "experimental_policy_template")
	if err != nil || template != original+"\n\n"+codexExtraPolicySlot+"\n" {
		t.Fatal("default template changed", template, err)
	}
	if err := installCodexReviewPolicy(consent); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if string(again) != string(data) {
		t.Fatal("repeat install changed config")
	}
	writeCatalog(original + "\nNew default protection.")
	proposal, _, err := inspectCodexReviewPolicy(consent)
	if err != nil || proposal.State != "migration_required" {
		t.Fatal("stale template not detected", proposal, err)
	}
	if err := installCodexReviewPolicy(consent); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(path)
	template, _ = codexAutoReviewString(updated, "experimental_policy_template")
	if !strings.Contains(template, "New default protection.") {
		t.Fatal("kept stale default instructions")
	}
}

func TestCodexTemplateRepairPreservesCustomAndSkipsCompatibleDefaults(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	custom, _ := patchCodexAutoReviewString(nil, "experimental_policy_template", "Custom safety instructions without a slot")
	if _, err := codexReviewerTemplateRepair(path, custom); err == nil {
		t.Fatal("silently changed a custom template")
	}
	compatible, _ := patchCodexAutoReviewString(nil, "experimental_policy_template", "Custom safety instructions\n"+codexExtraPolicySlot)
	if repair, err := codexReviewerTemplateRepair(path, compatible); err != nil || repair != "" {
		t.Fatal(repair, err)
	}
	raw, _ := json.Marshal(map[string]any{"models": []any{map[string]any{"slug": "codex-auto-review", "model_messages": map[string]any{"auto_review": map[string]any{"policy_template": "Default\n{{ tenant_policy_config }}\n" + codexExtraPolicySlot}}}}})
	os.WriteFile(filepath.Join(home, "models_cache.json"), raw, 0600)
	if repair, err := codexReviewerTemplateRepair(path, nil); err != nil || repair != "" {
		t.Fatal("overrode a supported default", repair, err)
	}
}

func TestCodexAutoReviewStringRejectsInvalidTemplateType(t *testing.T) {
	if _, err := codexAutoReviewString([]byte("[auto_review]\nexperimental_policy_template = 5\n"), "experimental_policy_template"); err == nil {
		t.Fatal("accepted non-string reviewer template")
	}
}
