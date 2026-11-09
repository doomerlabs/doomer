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

func TestCodexTemplateCatalogFailuresAndDesktopSelection(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		wantError bool
	}{
		{"malformed", `{`, true},
		{"unrecognized", `{"models":[{"slug":"codex-auto-review","model_messages":{"auto_review":{"policy_template":"unknown instructions"}}}]}`, true},
		{"other model", `{"models":[{"slug":"other-model"}]}`, false},
		{"empty template", `{"models":[{"slug":"codex-auto-review","model_messages":{"auto_review":{"policy_template":""}}}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "config.toml")
			if err := os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			repair, err := codexReviewerTemplateRepair(path, nil)
			if (err != nil) != tc.wantError || repair != "" {
				t.Fatal(repair, err)
			}
		})
	}
	t.Run("unreadable catalog", func(t *testing.T) {
		home := t.TempDir()
		os.Mkdir(filepath.Join(home, "models_cache.json"), 0700)
		if _, err := codexReviewerTemplateRepair(filepath.Join(home, "config.toml"), nil); err == nil {
			t.Fatal("ignored unreadable catalog")
		}
	})
	t.Run("desktop catalog takes precedence", func(t *testing.T) {
		t.Setenv("CODEX_INTERNAL_ORIGINATOR_OVERRIDE", "Codex Desktop")
		home := t.TempDir()
		path := filepath.Join(home, "config.toml")
		catalog := func(template string) []byte {
			raw, _ := json.Marshal(map[string]any{"models": []any{map[string]any{"slug": "codex-auto-review", "model_messages": map[string]any{"auto_review": map[string]any{"policy_template": template}}}}})
			return raw
		}
		os.WriteFile(filepath.Join(home, "models_cache.json"), catalog("wrong catalog {{ tenant_policy_config }}"), 0600)
		os.WriteFile(filepath.Join(home, "models_cache.codex_work_desktop.json"), catalog("desktop catalog {{ tenant_policy_config }}"), 0600)
		repair, err := codexReviewerTemplateRepair(path, nil)
		if err != nil || !strings.HasPrefix(repair, "desktop catalog") {
			t.Fatal(repair, err)
		}
	})
}

func TestCodexTemplateOwnershipAndMarker(t *testing.T) {
	template := "Custom {{ tenant_policy_config }} {{ extra_policy }}"
	data, err := patchCodexAutoReviewString([]byte("# unrelated\n"), "experimental_policy_template", template)
	if err != nil {
		t.Fatal(err)
	}
	marked := markCodexReviewerTemplate(data, template)
	marked = markCodexReviewerTemplate(marked, template)
	if strings.Count(string(marked), codexTemplateMarker) != 1 || !strings.Contains(string(marked), "# unrelated") {
		t.Fatal(string(marked))
	}
	changed := strings.Replace(string(marked), "Custom", "User modified", 1)
	home := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"models": []any{map[string]any{"slug": "codex-auto-review", "model_messages": map[string]any{"auto_review": map[string]any{"policy_template": "New default {{ tenant_policy_config }} {{ extra_policy }}"}}}}})
	os.WriteFile(filepath.Join(home, "models_cache.json"), raw, 0600)
	if repair, err := codexReviewerTemplateRepair(filepath.Join(home, "config.toml"), []byte(changed)); err != nil || repair != "" {
		t.Fatal("overwrote a user-edited template", repair, err)
	}
	if _, err := codexReviewerTemplateRepair(filepath.Join(home, "config.toml"), []byte("[broken")); err == nil {
		t.Fatal("ignored malformed settings")
	}
}
