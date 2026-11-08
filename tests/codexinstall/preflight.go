package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Onboarding keeps its existing fixture permissions. This regression starts a
// fresh coding session with the actual Codex reviewer and workspace sandbox.
func (j *journey) reviewPreflight() error {
	// This scenario isolates per-call preparation. Automatic hooks can submit
	// after a tool call before the agent has processed the no-upload instruction.
	// Hook-driven delivery is exercised by the other lifecycle scenarios.
	if err := os.Remove(agentHooksPath(j.c, j.home)); err != nil {
		return err
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
	c := j.c
	c.ReviewApprovals = c.Harness != "claude"
	j.result.Turns++
	turn, err := runAgent(j.ctx, c, j.repo, j.env,
		"Create hello.txt containing hello followed by a newline. Use the installed Doomer skill, including local preflight and review preparation. This smoke test permits setup and account checks but no submission of repository content. Report whether preparation succeeded and finish the local edit.", "", j.record)
	if err != nil {
		return err
	}
	j.result.Checks["no_repeated_policy_offer"] = !turn.PolicyOffered
	j.result.Checks["local_preflight_observed"] = false
	j.mu.Lock()
	for _, event := range j.result.Events[eventStart:] {
		if event.Kind == "Command" && strings.Contains(event.Text, `"local_only":true`) && strings.Contains(event.Text, `"account_verified":false`) {
			j.result.Checks["local_preflight_observed"] = true
		}
	}
	j.mu.Unlock()
	tasks, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	prepared := false
	for _, task := range tasks {
		if !seen[task.Handle] && task.Stage == "ready" && task.Purpose == "coding" {
			prepared = true
		}
	}
	content, readErr := os.ReadFile(filepath.Join(j.repo, "hello.txt"))
	j.result.Checks["local_edit_completed"] = readErr == nil && string(content) == "hello\n"
	reported := strings.ToLower(strings.Join(turn.Messages, "\n"))
	j.result.Checks["initialized_or_reported_blocker"] = prepared || strings.Contains(reported, "denied") || strings.Contains(reported, "blocked") || strings.Contains(reported, "restriction")
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
