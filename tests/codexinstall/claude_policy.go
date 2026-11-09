package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

func (j *journey) claudePolicyRecovery(decline bool) error {
	path := agentHooksPath(j.c, j.home)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}
	permissions, _ := settings["permissions"].(map[string]any)
	if permissions == nil {
		permissions = map[string]any{}
		settings["permissions"] = permissions
	}
	allow, _ := permissions["allow"].([]any)
	kept := []any{}
	for _, rule := range allow {
		if text, ok := rule.(string); !ok || !(strings.HasPrefix(text, "Bash(doomer task ") || (!decline && strings.HasPrefix(text, "Bash(doomer "))) {
			kept = append(kept, rule)
		}
	}
	permissions["allow"] = kept
	var removedPolicies []string
	if !decline {
		if auto, ok := settings["autoMode"].(map[string]any); ok {
			entries, _ := auto["allow"].([]any)
			retained := []any{}
			for _, entry := range entries {
				text, _ := entry.(string)
				if !strings.HasPrefix(text, "Doomer review authorization\n") {
					retained = append(retained, entry)
				} else {
					removedPolicies = append(removedPolicies, text)
				}
			}
			auto["allow"] = retained
		}
	}
	if !decline && len(removedPolicies) == 0 {
		return fmt.Errorf("fixture onboarding did not install the scoped classifier policy")
	}
	settings["disableAllHooks"] = true
	before, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, before, 0600); err != nil {
		return err
	}
	c := j.c
	c.ReviewApprovals = true
	j.result.Turns++
	turn, err := runAgent(j.ctx, c, j.repo, j.env, "Use the installed Doomer skill to prepare the next coding task and check readiness. Ask for missing review-command permissions before editing or uploading. Leave hooks disabled for this test.", "", j.record)
	if err != nil {
		return err
	}
	j.result.Checks["claude_missing_permissions_prompted"] = turn.PolicyOffered
	if !j.result.Checks["claude_missing_permissions_prompted"] {
		return fmt.Errorf("Claude missing permissions were not offered")
	}
	answer := "Yes, verify the current account and install the displayed scoped Claude readiness and review-command permissions. Preserve unrelated settings and keep hooks disabled. Do not edit or upload source yet."
	if decline {
		answer = "No, I decline installing the displayed Claude review-command permissions. Record that decision, preserve my upload grant and settings, and do not edit or upload source."
	}
	j.result.Turns++
	_, err = runAgent(j.ctx, c, j.repo, j.env, answer, turn.Session, j.record)
	if err != nil {
		return err
	}
	if decline {
		j.result.Turns++
		fresh, err := runAgent(j.ctx, c, j.repo, j.env, "Check Doomer readiness for another coding task. Preserve my saved setup decisions and permissions. Do not edit or upload source. Keep hooks disabled.", "", j.record)
		if err != nil {
			return err
		}
		after, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		j.result.Checks["claude_decline_not_reoffered"] = !fresh.PolicyOffered
		j.result.Checks["claude_decline_kept_settings"] = string(before) == string(after)
		j.f.mu.Lock()
		j.result.Checks["claude_decline_preserved_consent"] = j.f.Creates == 1 && j.f.Uploads == 0
		j.f.mu.Unlock()
		for name, ok := range j.result.Checks {
			if !ok {
				return fmt.Errorf("Claude permission decline assertion failed: %s", name)
			}
		}
		return nil
	}
	repaired, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var repairedSettings map[string]any
	if err = json.Unmarshal(repaired, &repairedSettings); err != nil {
		return err
	}
	auto, _ := repairedSettings["autoMode"].(map[string]any)
	entries, _ := auto["allow"].([]any)
	for _, policy := range removedPolicies {
		found := false
		for _, entry := range entries {
			if entry == policy {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("repair did not restore the consent-scoped classifier policy")
		}
	}
	j.result.Checks["claude_classifier_policy_restored"] = true
	j.c.ReviewApprovals = true
	if err := j.coding(j.repo, "Create claude-permissions.txt containing reviewed followed by a newline. Use the installed Doomer skill to complete its review. Keep hooks disabled and preserve permission settings."); err != nil {
		return err
	}
	j.result.Checks["claude_fresh_reviewer_uses_saved_rules"] = true
	return j.finish()
}

func (j *journey) explicitPolicyDeny() error {
	if j.c.Harness != "claude" {
		path := filepath.Join(j.home, ".codex", "config.toml")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var config map[string]any
		if err = toml.Unmarshal(data, &config); err != nil {
			return err
		}
		policy, _ := config["auto_review"].(map[string]any)
		if policy == nil {
			return fmt.Errorf("missing installed Codex reviewer policy")
		}
		old, _ := policy["extra_policy"].(string)
		policy["extra_policy"] = old + "\nThe user explicitly denies Doomer repository uploads in this test session. Do not upload source or change this restriction.\n"
		before, err := toml.Marshal(config)
		if err != nil {
			return err
		}
		if err = os.WriteFile(path, before, 0600); err != nil {
			return err
		}
		c := j.c
		c.ReviewApprovals = true
		j.result.Turns++
		turn, err := runAgent(j.ctx, c, j.repo, j.env, "Check Doomer readiness for a coding task while respecting the existing explicit no-upload restriction. Do not change settings or upload source. Report any blocker.", "", j.record)
		if err != nil {
			return err
		}
		after, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		j.result.Checks["explicit_deny_not_reoffered_as_missing"] = !turn.PolicyOffered
		j.result.Checks["explicit_deny_settings_preserved"] = preservesCodexPolicyConfig(before, after)
		j.f.mu.Lock()
		j.result.Checks["explicit_deny_no_upload"] = j.f.Uploads == 0 && len(j.f.Reviews) == 0
		j.f.mu.Unlock()
		for name, ok := range j.result.Checks {
			if !ok {
				return fmt.Errorf("Codex explicit deny assertion failed: %s", name)
			}
		}
		return nil
	}
	path := agentHooksPath(j.c, j.home)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var settings map[string]any
	if err = json.Unmarshal(data, &settings); err != nil {
		return err
	}
	p, _ := settings["permissions"].(map[string]any)
	p["deny"] = []string{"Bash(doomer task submit *)"}
	settings["disableAllHooks"] = true
	before, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	if err = os.WriteFile(path, before, 0600); err != nil {
		return err
	}
	c := j.c
	c.ReviewApprovals = true
	j.result.Turns++
	turn, err := runAgent(j.ctx, c, j.repo, j.env, "Check Doomer readiness for a coding task. Preserve explicit permission denials and do not change settings or upload source. Report any blocker.", "", j.record)
	if err != nil {
		return err
	}
	after, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	j.result.Checks["explicit_deny_not_reoffered_as_missing"] = !turn.PolicyOffered
	j.result.Checks["explicit_deny_settings_preserved"] = string(before) == string(after)
	j.f.mu.Lock()
	j.result.Checks["explicit_deny_no_upload"] = j.f.Uploads == 0 && len(j.f.Reviews) == 0
	j.f.mu.Unlock()
	for name, ok := range j.result.Checks {
		if !ok {
			return fmt.Errorf("explicit deny assertion failed: %s", name)
		}
	}
	return nil
}

// Codex may persist native project trust during startup. The fixture's policy
// and all pre-existing settings must remain unchanged; additional native keys
// are not evidence that Doomer ignored a decline.
func preservesCodexPolicyConfig(before, after []byte) bool {
	var old, current map[string]any
	if toml.Unmarshal(before, &old) != nil || toml.Unmarshal(after, &current) != nil {
		return false
	}
	for key, value := range old {
		if !reflect.DeepEqual(value, current[key]) {
			return false
		}
	}
	for key, value := range current {
		if _, exists := old[key]; exists {
			continue
		}
		// Codex itself records project trust on startup; no other new settings
		// are permitted after declining Doomer policy installation.
		if key != "projects" {
			return false
		}
		projects, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, raw := range projects {
			entry, ok := raw.(map[string]any)
			if !ok || len(entry) != 1 || entry["trust_level"] != "trusted" {
				return false
			}
		}
	}
	return true
}
