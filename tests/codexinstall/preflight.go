package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/internal/watch"
)

// Onboarding keeps its existing fixture permissions. This regression starts a
// fresh coding session with the actual Codex reviewer and workspace sandbox.
func (j *journey) reviewPreflight() error {
	// This scenario isolates per-call preparation. Automatic hooks can submit
	// after a tool call before the agent has processed the no-upload instruction.
	// Hook-driven delivery is exercised by the other lifecycle scenarios.
	if err := watch.Install(agentHooksPath(j.c, j.home), "", "--managed-task-adapter", true); err != nil && !os.IsNotExist(err) {
		return err
	}
	if j.c.Scenario == "review-preflight-denied" && j.c.Harness == "claude" {
		data, err := os.ReadFile(agentHooksPath(j.c, j.home))
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
		}
		deny, _ := permissions["deny"].([]any)
		permissions["deny"] = append(deny, "Bash(doomer prepare *)", "Bash(*doomer* prepare *)")
		settings["permissions"] = permissions
		data, err = json.Marshal(settings)
		if err != nil {
			return err
		}
		if err := os.WriteFile(agentHooksPath(j.c, j.home), data, 0600); err != nil {
			return err
		}
	}
	prior, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, task := range prior {
		seen[task.Handle] = true
	}
	eventStart := len(j.result.Events)
	baseline, err := exec.CommandContext(j.ctx, "git", "-C", j.repo, "rev-parse", "HEAD").Output()
	if err != nil {
		return err
	}
	c := j.c
	c.ReviewApprovals = c.Harness != "claude" || j.c.Scenario == "review-preflight-denied"
	j.result.Turns++
	prompt := "Create hello.txt containing hello followed by a newline. This is a coding task, not setup-only onboarding. Use the installed Doomer skill: run local preflight, then prepare a coding task with --prompt describing this edit. Preparation and account checks are allowed, but do not submit repository content or enable hooks for this test. Report whether preparation succeeded and finish the local edit."
	if j.c.Scenario == "review-preflight-denied" {
		prompt += " I deny any permission request needed for Doomer preparation in this run. Do not request elevation, retry a denied operation, or change settings. If preparation cannot run, complete the local edit and give me its exact terminal recovery command."
	}
	turn, err := runAgent(j.ctx, c, j.repo, j.env,
		prompt, "", j.record)
	if err != nil {
		return err
	}
	j.result.Checks["no_repeated_policy_offer"] = !turn.PolicyOffered
	j.result.Checks["local_preflight_observed"] = false
	j.result.Checks["local_preflight_reuses_agent_consent"] = false
	j.mu.Lock()
	for _, event := range j.result.Events[eventStart:] {
		localOnly, reused := preflightEvidence(event.Text)
		if event.Kind == "Command" && localOnly {
			j.result.Checks["local_preflight_observed"] = true
			j.result.Checks["local_preflight_reuses_agent_consent"] = j.result.Checks["local_preflight_reuses_agent_consent"] || reused
		}
	}
	j.mu.Unlock()
	tasks, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	prepared := false
	j.result.Checks["coding_task_uses_current_agent"] = true
	for _, task := range tasks {
		if !seen[task.Handle] && task.Agent != j.c.Harness {
			j.result.Checks["coding_task_uses_current_agent"] = false
		}
		if !seen[task.Handle] && task.Stage == "ready" && task.Purpose == "coding" {
			prepared = true
		}
	}
	content, readErr := os.ReadFile(filepath.Join(j.repo, "hello.txt"))
	j.result.Checks["local_edit_completed"] = readErr == nil && string(content) == "hello\n"
	reported := strings.ToLower(strings.Join(turn.Messages, "\n"))
	if j.c.Scenario == "review-preflight-denied" {
		j.result.Checks["denied_preparation_did_not_create_task"] = len(tasks) == len(prior)
		j.result.Checks["actionable_recovery_reported"] = strings.Contains(reported, "doomer") && strings.Contains(reported, "prepare") && strings.Contains(reported, "--prompt") && strings.Contains(reported, "--base") && strings.Contains(reported, strings.TrimSpace(string(baseline)))
		j.result.Checks["recovery_identifies_checkout"] = strings.Contains(reported, strings.ToLower(j.repo))
	} else {
		// An authorized fixture must actually prepare. A polite blocker report
		// is a separate negative scenario, not a passing recovery test.
		j.result.Checks["authorized_preparation_completed"] = prepared
	}
	j.record("fixture", fmt.Sprintf("Fresh session preparation completed: %t", prepared))
	j.f.mu.Lock()
	j.result.Checks["original_grant_reused"] = j.f.Creates == 1
	j.result.Checks["no_content_uploaded"] = j.f.Uploads == 0 && len(j.f.Reviews) == 0
	j.f.mu.Unlock()
	if c.ReviewApprovals {
		j.record("fixture", "Fresh session used workspace-write, on-request approval, and auto_review; no approval bypass flag.")
	}
	for name, ok := range j.result.Checks {
		if !ok {
			return fmt.Errorf("review preflight assertion failed: %s", name)
		}
	}
	// The user explicitly prohibited content submission for this smoke test.
	return nil
}

// Tool transcripts can prefix the JSON with the command or format its whitespace.
func preflightEvidence(text string) (localOnly, reused bool) {
	for start := 0; start < len(text); start++ {
		if text[start] != '{' {
			continue
		}
		var result struct {
			LocalOnly       bool   `json:"local_only"`
			AccountVerified *bool  `json:"account_verified"`
			State           string `json:"state"`
		}
		if json.NewDecoder(strings.NewReader(text[start:])).Decode(&result) == nil && result.LocalOnly && result.AccountVerified != nil && !*result.AccountVerified {
			localOnly = true
			reused = reused || result.State == "saved_consent_found"
		}
	}
	return
}
