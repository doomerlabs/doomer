package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
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
	allow, _ := permissions["allow"].([]any)
	kept := []any{}
	for _, rule := range allow {
		if text, ok := rule.(string); !ok || !strings.HasPrefix(text, "Bash(doomer task ") {
			kept = append(kept, rule)
		}
	}
	permissions["allow"] = kept
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
	if !turn.PolicyOffered {
		return fmt.Errorf("Claude missing permissions were not offered")
	}
	answer := "Yes, install the displayed scoped Claude review-command permissions. Preserve unrelated settings and keep hooks disabled. Do not edit or upload source yet."
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
	j.c.ReviewApprovals = true
	if err := j.coding(j.repo, "Create claude-permissions.txt containing reviewed followed by a newline. Use the installed Doomer skill to complete its review. Keep hooks disabled and preserve permission settings."); err != nil {
		return err
	}
	j.result.Checks["claude_fresh_reviewer_uses_saved_rules"] = true
	return j.finish()
}
