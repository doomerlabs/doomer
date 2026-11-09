package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/internal/runtimeversion"
)

// Exercise the reviewer itself in a new session; a configured file is not proof
// that the policy was included in the approval model's trusted instructions.
func (j *journey) policyActivation(run func(string, ...string) ([]byte, error)) error {
	tasks, err := savedTasks(j.home)
	if err != nil || len(tasks) != 1 {
		return fmt.Errorf("read seed task: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(j.c.CodexAuthHome, "models_cache.codex_work_desktop.json"))
	if os.IsNotExist(err) {
		raw, err = os.ReadFile(filepath.Join(j.c.CodexAuthHome, "models_cache.json"))
	}
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(j.home, ".codex/models_cache.json"), raw, 0600); err != nil {
		return err
	}
	binary := runtimeversion.Binary(j.home, j.c.CandidateVersion)
	if _, err = run(binary, "task", "codex-policy", tasks[0].Handle, "--allow"); err != nil {
		return err
	}
	if _, err = run(binary, "task", "hook-setup", tasks[0].Handle, "--decline"); err != nil {
		return err
	}
	if _, err = run(binary, "bootstrap", "upgrade", "--repo", j.repo, "--accept"); err != nil {
		return err
	}
	if err = j.git(j.repo, "add", "."); err != nil {
		return err
	}
	if err = j.git(j.repo, "-c", "commit.gpgsign=false", "commit", "-qm", "Complete fixture onboarding"); err != nil {
		return err
	}
	j.c.ReviewApprovals = true
	j.result.Turns++
	turn, err := runAgent(j.ctx, j.c, j.repo, j.env, "Remove Waitlist from the admin navigation and make a PR.", "", j.record)
	if err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(j.home, ".codex/sessions/*/*/*/*.jsonl"))
	delivered := false
	for _, path := range files {
		data, _ := os.ReadFile(path)
		for _, line := range strings.Split(string(data), "\n") {
			var event struct {
				Type    string `json:"type"`
				Payload struct {
					Role    string `json:"role"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"payload"`
			}
			if json.Unmarshal([]byte(line), &event) != nil || event.Type != "response_item" || (event.Payload.Role != "developer" && event.Payload.Role != "system") {
				continue
			}
			for _, block := range event.Payload.Content {
				if strings.Contains(block.Text, "Doomer repository review authorization") {
					delivered = true
				}
			}
		}
	}
	j.f.mu.Lock()
	j.result.Checks["fresh_session_submitted_without_new_grant"] = j.f.Uploads > 0 && j.f.Creates == 1
	j.f.mu.Unlock()
	j.result.Checks["policy_delivered_to_reviewer"] = delivered
	j.result.Checks["no_repeated_policy_offer"] = !turn.PolicyOffered
	for k, v := range j.result.Checks {
		if !v {
			return fmt.Errorf("policy activation failed: %s", k)
		}
	}
	return nil
}
