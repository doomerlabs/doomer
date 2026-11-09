package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const codexExtraPolicySlot = "{{ extra_policy }}"
const codexTemplateMarker = "# doomer-review-template-sha256: "

// Some released Codex catalogs omit the extra-policy slot. Keep every byte of
// their template and add only that slot. Never replace an unowned custom template.
func codexReviewerTemplateRepair(configPath string, data []byte) (string, error) {
	current, err := codexAutoReviewString(data, "experimental_policy_template")
	if err != nil {
		return "", err
	}
	managed := current != "" && bytes.Contains(data, []byte(codexTemplateMarker+codexTemplateDigest(current)))
	if current != "" && !managed {
		if !strings.Contains(current, codexExtraPolicySlot) {
			return "", fmt.Errorf("custom Codex reviewer template does not expose extra_policy; preserve it and request a scoped template repair")
		}
		return "", nil
	}
	names := []string{"models_cache.json"}
	if strings.Contains(strings.ToLower(os.Getenv("CODEX_INTERNAL_ORIGINATOR_OVERRIDE")), "desktop") {
		names = []string{"models_cache.codex_work_desktop.json", "models_cache.json"}
	}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		var catalog struct {
			Models []struct {
				Slug          string `json:"slug"`
				ModelMessages struct {
					AutoReview *struct {
						PolicyTemplate string `json:"policy_template"`
					} `json:"auto_review"`
				} `json:"model_messages"`
			} `json:"models"`
		}
		if err := json.Unmarshal(raw, &catalog); err != nil {
			return "", fmt.Errorf("read Codex reviewer catalog: %w", err)
		}
		for _, model := range catalog.Models {
			if model.Slug != "codex-auto-review" || model.ModelMessages.AutoReview == nil {
				continue
			}
			template := model.ModelMessages.AutoReview.PolicyTemplate
			if template == "" {
				continue
			}
			if !strings.Contains(template, "{{ tenant_policy_config }}") {
				return "", fmt.Errorf("unrecognized Codex reviewer template; no settings changed")
			}
			if !strings.Contains(template, codexExtraPolicySlot) {
				template += "\n\n" + codexExtraPolicySlot + "\n"
			} else if current == "" {
				return "", nil
			}
			if template == current {
				return "", nil
			}
			return template, nil
		}
	}
	return "", nil
}

func codexTemplateDigest(template string) string {
	sum := sha256.Sum256([]byte(template))
	return hex.EncodeToString(sum[:])
}

func markCodexReviewerTemplate(data []byte, template string) []byte {
	lines := bytes.Split(data, []byte("\n"))
	kept := lines[:0]
	for _, line := range lines {
		if !bytes.HasPrefix(line, []byte(codexTemplateMarker)) {
			kept = append(kept, line)
		}
	}
	out := bytes.Join(kept, []byte("\n"))
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, []byte(codexTemplateMarker+codexTemplateDigest(template)+"\n")...)
}
