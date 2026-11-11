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

// Reproduce a previous chat that read the proposal but never installed it.
// The fixture alone removes settings; the real agent must repair them through
// the proposed command after a scripted user accepts the missing setup.
func (j *journey) policyRecovery() error {
	if j.c.Harness != "codex" {
		return j.claudePolicyRecovery(false)
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
	if err := json.Unmarshal(raw, &state); err != nil || state.Proposal.ExtraPolicy == "" || state.Proposal.ConfigPath != filepath.Join(j.home, ".codex", "config.toml") {
		return fmt.Errorf("missing fixture proposal: %v", err)
	}
	oldMarker := filepath.Join(j.home, ".config/doomer/managed/codex-policy-offers", watch.Key("config-v2\x00"+state.Proposal.ConfigPath+"\x00\x00"+state.Proposal.ExtraPolicy))
	if err := os.MkdirAll(filepath.Dir(oldMarker), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(oldMarker, []byte("delivered, not installed\n"), 0600); err != nil {
		return err
	}
	prepared, err := j.policyTask(tasks[0].Handle, "prepare")
	if err != nil {
		return err
	}
	// This deterministic assertion is also run against the released CLI. It
	// must fail for the precise regression, not a provider or install failure.
	if prepared.Proposal == nil {
		return fmt.Errorf("deterministic prepare probe: missing policy after delivered offer")
	}
	c := j.c
	c.ReviewApprovals = true
	j.result.Turns++
	attemptedPrepare := false
	record := func(kind, text string) {
		j.record(kind, text)
		if kind == "Command started" && strings.Contains(text, "doomer") && strings.Contains(text, "prepare") {
			attemptedPrepare = true
		}
	}
	// This request probes the missing-policy gate. Completion is tested in a
	// separate fresh source task after the policy and hooks are repaired below.
	turn, err := runAgent(j.ctx, c, j.repo, j.env, "Create recovered.py containing print('recovered') followed by a newline.", "", record)
	if err != nil {
		return err
	}
	offered := policyChoiceOffered(strings.Join(turn.Messages, " "))
	j.result.Checks["missing_policy_offered_from_ordinary_task"] = offered
	j.result.Checks["policy_prompt_before_prepare"] = !attemptedPrepare
	j.f.mu.Lock()
	j.result.Checks["no_upload_before_policy_answer"] = j.f.Uploads == 0
	j.f.mu.Unlock()
	if !offered || attemptedPrepare {
		return fmt.Errorf("missing policy was not offered after old delivery")
	}
	j.result.Turns++
	accepted, err := runAgent(j.ctx, c, j.repo, j.env, "Allow the proposed scoped reviewer policy and account verification.", turn.Session, j.record)
	if err != nil {
		return err
	}
	repaired, err := j.policyTask(tasks[0].Handle, "prepare")
	if err != nil {
		return err
	}
	if repaired.Status.State != "configured" {
		return fmt.Errorf("accepted Codex policy repair did not install the policy")
	}
	j.result.Checks["accepted_policy_repair_installed"] = true
	j.result.Checks["missing_hooks_offered_separately"] = accepted.HooksOffered && strings.Contains(strings.ToLower(strings.Join(accepted.Messages, " ")), "hook")
	if !j.result.Checks["missing_hooks_offered_separately"] {
		return fmt.Errorf("missing hooks were not offered after policy installation")
	}
	j.result.Turns++
	if _, err := runAgent(j.ctx, c, j.repo, j.env, "Allow installation of the proposed Doomer hooks. Leave native hook trust for me to approve in the host.", turn.Session, j.record); err != nil {
		return err
	}
	installed, err := watch.Installed(agentHooksPath(j.c, j.home), "--managed-task-adapter")
	if err != nil || !installed {
		return fmt.Errorf("accepted hook installation missing: %v", err)
	}
	j.result.Checks["accepted_hooks_installed"] = true
	// A separate reviewer process must load the repaired policy and complete a
	// real submission without another upload grant or settings prompt.
	j.c.ReviewApprovals = true
	if err := j.coding(j.repo, "Create finished.py containing print('finished') followed by a newline."); err != nil {
		return err
	}
	if err := j.verifyPythonProgram(filepath.Join(j.repo, "finished.py"), "finished\n"); err != nil {
		return err
	}
	j.result.Checks["fresh_source_program_runs"] = true
	j.result.Checks["fresh_reviewer_completed_after_policy_repair"] = true
	return j.finish()
}

type policyTaskState struct {
	Proposal *json.RawMessage `json:"codex_review_policy"`
	Status   struct {
		State    string `json:"state"`
		Decision string `json:"decision"`
	} `json:"codex_policy_status"`
}

func (j *journey) policyTask(handle, operation string) (policyTaskState, error) {
	args := []string{"task", "codex-policy", handle}
	if operation == "prepare" {
		args = []string{"prepare", "--task", handle}
	}
	cmd := exec.CommandContext(j.ctx, filepath.Join(j.home, ".local/bin/doomer"), args...)
	cmd.Dir, cmd.Env = j.repo, j.env
	raw, err := cmd.Output()
	var state policyTaskState
	if err == nil {
		err = json.Unmarshal(raw, &state)
	}
	return state, err
}

func (j *journey) policyDeclined() error {
	if j.c.Harness != "codex" {
		return j.claudePolicyRecovery(true)
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
	c := j.c
	c.ReviewApprovals = true
	j.result.Turns++
	turn, err := runAgent(j.ctx, c, j.repo, j.env, "Use the installed Doomer skill to prepare a coding task. You may verify my existing account and grant. Ask about missing policy settings, but do not edit or upload source yet. Leave hooks disabled for this test.", "", j.record)
	if err != nil {
		return err
	}
	if !turn.PolicyOffered {
		return fmt.Errorf("missing policy was not offered before decline")
	}
	j.result.Turns++
	_, err = runAgent(j.ctx, c, j.repo, j.env, "No. I decline installing this Codex policy. Preserve my settings and existing Doomer upload grant. Record my decision using the returned command. Do not edit or upload source.", turn.Session, j.record)
	if err != nil {
		return err
	}
	state, err := j.policyTask(tasks[0].Handle, "prepare")
	if err != nil {
		return err
	}
	j.result.Checks["explicit_decline_persisted"] = state.Status.State == "missing" && state.Status.Decision == "declined" && state.Proposal == nil
	j.result.Turns++
	fresh, err := runAgent(j.ctx, c, j.repo, j.env, "Use the installed Doomer skill to prepare a new coding task. You may verify my account and existing grant. Do not edit or upload source for this check. Preserve settings and report readiness.", "", j.record)
	if err != nil {
		return err
	}
	j.result.Checks["decline_not_reoffered_in_new_chat"] = !fresh.PolicyOffered
	config, err := os.ReadFile(filepath.Join(j.home, ".codex", "config.toml"))
	if err != nil {
		return err
	}
	j.result.Checks["declined_config_unchanged"] = preservesCodexPolicyConfig([]byte(j.codexConfig), config)
	j.f.mu.Lock()
	j.result.Checks["decline_preserved_grant_without_upload"] = j.f.Creates == 1 && j.f.Uploads == 0 && len(j.f.Reviews) == 0
	j.f.mu.Unlock()
	for name, ok := range j.result.Checks {
		if !ok {
			return fmt.Errorf("policy decline assertion failed: %s", name)
		}
	}
	return nil
}
