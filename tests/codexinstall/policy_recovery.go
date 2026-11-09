package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/doomerlabs/doomer/internal/watch"
)

// Reproduce a previous chat that read the proposal but never installed it.
// The fixture alone removes settings; the real agent must repair them through
// the proposed command after a scripted user accepts the missing setup.
func (j *journey) policyRecovery() error {
	if j.c.Harness != "codex" {
		return j.reviewCommandPermissions()
	}
	if err := os.Remove(agentHooksPath(j.c, j.home)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := writeAgentConfig(j.c, j.home, j.codexConfig); err != nil {
		return err
	}
	tasks, err := savedTasks(j.home)
	if err != nil || len(tasks) != 1 {
		return fmt.Errorf("read onboarding task: %v (%d tasks)", err, len(tasks))
	}
	cmd := exec.CommandContext(j.ctx, filepath.Join(j.home, ".local/bin/doomer"), "task", "codex-policy", tasks[0].Handle)
	cmd.Dir, cmd.Env = j.repo, j.env
	raw, err := cmd.Output()
	if err != nil {
		return err
	}
	var state struct {
		Proposal struct {
			ConfigPath  string `json:"config_path"`
			ExtraPolicy string `json:"extra_policy"`
		} `json:"codex_review_policy"`
	}
	if err := json.Unmarshal(raw, &state); err != nil || state.Proposal.ExtraPolicy == "" {
		return fmt.Errorf("missing fixture proposal: %v", err)
	}
	oldMarker := filepath.Join(j.home, ".config/doomer/managed/codex-policy-offers", watch.Key("config-v2\x00"+state.Proposal.ConfigPath+"\x00\x00"+state.Proposal.ExtraPolicy))
	if err := os.MkdirAll(filepath.Dir(oldMarker), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(oldMarker, []byte("delivered, not installed\n"), 0600); err != nil {
		return err
	}
	c := j.c
	c.ReviewApprovals = true
	j.result.Turns++
	turn, err := runAgent(j.ctx, c, j.repo, j.env, "Use the installed Doomer skill to prepare a code-review task for creating recovered.txt. You may run local preflight and verify the existing account and consent. Ask for any missing setup choice before uploading source; do not create the file yet.", "", j.record)
	if err != nil {
		return err
	}
	j.result.Checks["missing_policy_offered_after_old_delivery"] = turn.PolicyOffered
	if !turn.PolicyOffered {
		return fmt.Errorf("missing policy was not offered after old delivery")
	}
	j.result.Turns++
	_, err = runAgent(j.ctx, c, j.repo, j.env, "I approve saving the displayed scoped Doomer authorization in Codex's approval-reviewer settings. Apply it and verify the configuration. Do not upload or edit source yet.", turn.Session, j.record)
	if err != nil {
		return err
	}
	// A separate reviewer process must load the repaired policy and complete a
	// real submission without another upload grant or settings prompt.
	j.c.ReviewApprovals = true
	if err := j.coding(j.repo, "Create recovered.txt containing recovered followed by a newline. Use the installed Doomer skill to review the change; leave hooks disabled."); err != nil {
		return err
	}
	j.result.Checks["fresh_reviewer_completed_after_policy_repair"] = true
	return j.finish()
}
