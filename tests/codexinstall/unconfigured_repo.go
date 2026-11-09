package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/internal/watch"
)

// An installation in one checkout must not enlist unrelated checkouts through
// its profile-wide hooks. Exercise the installed binary boundary and then an
// ordinary real-agent task, without telling the agent to skip Doomer.
func (j *journey) unconfiguredRepo() error {
	for _, dir := range []string{".agents", ".claude"} {
		if err := os.RemoveAll(filepath.Join(j.home, dir, "skills/doomer-review")); err != nil {
			return err
		}
	}
	plain := filepath.Join(filepath.Dir(j.repo), "unconfigured")
	if err := os.MkdirAll(plain, 0700); err != nil {
		return err
	}
	if err := j.git(plain, "init", "-q"); err != nil {
		return err
	}
	if err := j.git(plain, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "Unconfigured fixture"); err != nil {
		return err
	}
	linked := filepath.Join(filepath.Dir(j.repo), "unconfigured-linked")
	if err := j.git(plain, "worktree", "add", "-b", "unconfigured-linked", linked); err != nil {
		return err
	}
	before, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	settings, err := os.ReadFile(agentHooksPath(j.c, j.home))
	if err != nil {
		return err
	}
	j.f.mu.Lock()
	eventsBefore := len(j.f.Events)
	j.f.mu.Unlock()
	for i, repo := range []string{plain, linked} {
		for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
			input, _ := json.Marshal(watch.HookInput{CWD: repo, Session: fmt.Sprintf("unconfigured-%d", i), Event: event, Prompt: "Create hello.txt containing hello followed by a newline."})
			cmd := exec.CommandContext(j.ctx, filepath.Join(j.home, ".local/bin/doomer"), "agent-hook", "--agent", agentName(j.c), "--managed-task-adapter")
			cmd.Dir = repo
			cmd.Env = j.env
			cmd.Stdin = bytes.NewReader(input)
			out, err := cmd.Output()
			if err != nil {
				return fmt.Errorf("unconfigured hook: %w", err)
			}
			var response map[string]any
			if json.Unmarshal(out, &response) != nil || len(response) != 0 {
				return fmt.Errorf("hook enlisted unconfigured repository: %s", out)
			}
		}
		j.result.Turns++
		turn, err := runAgent(j.ctx, j.c, repo, j.env, "Create hello.txt containing hello followed by a newline.", "", j.record)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(filepath.Join(repo, "hello.txt"))
		if err != nil || string(content) != "hello\n" {
			return fmt.Errorf("ordinary coding task did not complete")
		}
		// Fixture paths contain "doomer"; a link to the created file is not a setup offer.
		messages := strings.ReplaceAll(strings.Join(turn.Messages, " "), repo, "<repo>")
		if turn.HooksOffered || turn.PolicyOffered || turn.UpdateRequested || strings.Contains(strings.ToLower(messages), "doomer") {
			return fmt.Errorf("agent offered unexpected Doomer setup")
		}
	}
	after, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	afterSettings, err := os.ReadFile(agentHooksPath(j.c, j.home))
	if err != nil {
		return err
	}
	j.f.mu.Lock()
	eventsAfter := len(j.f.Events)
	j.f.mu.Unlock()
	j.result.Checks["unconfigured_hooks_silent"] = true
	j.result.Checks["unconfigured_coding_completed"] = true
	j.result.Checks["unconfigured_no_tasks"] = len(before) == len(after)
	j.result.Checks["unconfigured_no_api_calls"] = eventsBefore == eventsAfter
	j.result.Checks["unconfigured_settings_preserved"] = bytes.Equal(settings, afterSettings)
	for name, ok := range j.result.Checks {
		if !ok {
			return fmt.Errorf("unconfigured repository assertion failed: %s", name)
		}
	}
	return nil
}
