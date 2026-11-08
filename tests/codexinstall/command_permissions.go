package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// A fresh real-agent process must complete per-call reviews through its normal
// permission system. Hooks are absent so they cannot hide a denied submit.
func (j *journey) reviewCommandPermissions() error {
	path := agentHooksPath(j.c, j.home)
	if j.c.Harness == "claude" {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var settings map[string]any
		if err = json.Unmarshal(data, &settings); err != nil {
			return err
		}
		permissions, _ := settings["permissions"].(map[string]any)
		allow, _ := permissions["allow"].([]any)
		found := map[string]bool{}
		for _, rule := range allow {
			if value, ok := rule.(string); ok {
				found[value] = true
			}
		}
		for _, operation := range []string{"submit", "status", "respond", "finish"} {
			if !found["Bash(doomer task "+operation+" *)"] {
				return fmt.Errorf("onboarding did not install the %s permission rule", operation)
			}
		}
		delete(settings, "hooks")
		data, err = json.Marshal(settings)
		if err != nil {
			return err
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			return err
		}
	} else if err := os.Remove(path); err != nil {
		return err
	}
	j.c.ReviewApprovals = true
	j.f.mu.Lock()
	j.f.Findings = true
	j.f.mu.Unlock()
	j.result.Checks["hooks_disabled_for_command_permissions"] = true
	j.record("fixture", "Fresh process with hooks disabled; use native command approval rather than bypassPermissions")
	if err := j.coding(j.repo, "Create command-review.txt containing reviewed followed by a newline. Use the installed Doomer skill to complete the review through its task commands; leave hooks disabled and keep permission settings unchanged."); err != nil {
		return err
	}
	j.result.Checks["explicit_review_commands_completed"] = true
	return j.finish()
}
