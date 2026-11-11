package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/watch"
)

func (j *journey) hookSetupRecovery() error {
	path := agentHooksPath(j.c, j.home)
	if err := watch.Install(path, "", "--managed-task-adapter", true); err != nil {
		return err
	}
	before, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	c := j.c
	c.ReviewApprovals = true
	j.result.Turns++
	turn, err := runAgent(j.ctx, c, j.repo, j.env, "Check the installed Doomer integration before the next coding task. Preserve existing upload approval. Ask for any missing setup before editing or uploading source.", "", j.record)
	if err != nil {
		return err
	}
	j.result.Checks["missing_hooks_prompted"] = turn.HooksOffered && strings.Contains(strings.ToLower(strings.Join(turn.Messages, " ")), "hook")
	if !j.result.Checks["missing_hooks_prompted"] {
		return fmt.Errorf("missing hooks were not offered")
	}
	answer := "Yes, install the displayed Doomer hooks and the scoped Claude review-command rules when applicable. Preserve unrelated settings and native trust controls. Do not edit or upload source yet."
	if j.c.Scenario == "hooks-declined" {
		answer = "No, do not install Doomer hooks. Record this installation decline and preserve my upload grant and all settings. Do not edit or upload source."
	}
	j.result.Turns++
	_, err = runAgent(j.ctx, c, j.repo, j.env, answer, turn.Session, j.record)
	if err != nil {
		return err
	}
	if j.c.Scenario == "hooks-declined" {
		j.result.Turns++
		fresh, err := runAgent(j.ctx, c, j.repo, j.env, "Check readiness for the next Doomer coding task. Preserve existing setup decisions and permissions. Do not edit or upload any source.", "", j.record)
		if err != nil {
			return err
		}
		after, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		j.result.Checks["declined_hooks_not_reoffered"] = !fresh.HooksOffered
		j.result.Checks["declined_hooks_unchanged"] = bytes.Equal(before, after)
	} else {
		installed, err := watch.Installed(path, "--managed-task-adapter")
		if err != nil {
			return err
		}
		j.result.Checks["accepted_hooks_installed"] = installed
		if j.c.Harness == "claude" {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var settings struct {
				Permissions struct {
					Allow []string `json:"allow"`
				} `json:"permissions"`
			}
			if err := json.Unmarshal(data, &settings); err != nil {
				return err
			}
			for _, op := range []string{"submit", "status", "respond", "finish"} {
				j.result.Checks["claude_permission_"+op] = strings.Contains(strings.Join(settings.Permissions.Allow, "\n"), "Bash(doomer task "+op+" *)")
			}
		}
	}
	j.f.mu.Lock()
	j.result.Checks["hook_setup_reuses_grant_without_upload"] = j.f.Creates == 1 && j.f.Uploads == 0 && len(j.f.Reviews) == 0
	j.f.mu.Unlock()
	for name, ok := range j.result.Checks {
		if !ok {
			return fmt.Errorf("hook setup assertion failed: %s", name)
		}
	}
	return nil
}

func (j *journey) hookTrustRecovery() error {
	if j.c.Harness == "claude" {
		path := agentHooksPath(j.c, j.home)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var settings map[string]any
		if err = json.Unmarshal(data, &settings); err != nil {
			return err
		}
		settings["disableAllHooks"] = true
		disabled, _ := json.Marshal(settings)
		if err = os.WriteFile(path, disabled, 0600); err != nil {
			return err
		}
		c := j.c
		c.ReviewApprovals = true
		for attempt := 1; attempt <= 2; attempt++ {
			j.result.Turns++
			turn, err := runAgent(j.ctx, c, j.repo, j.env, "Check Doomer readiness before the next coding task. Do not edit or upload source, and do not change my settings.", "", j.record)
			if err != nil {
				return err
			}
			j.result.Checks[fmt.Sprintf("claude_disabled_hook_reminder_%d", attempt)] = claudeDisabledHookReminder(strings.Join(turn.Messages, " "))
			after, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(after, disabled) {
				return fmt.Errorf("Claude enabled hooks without acceptance")
			}
		}
		// The scripted user changes the native setting, then a real terminal
		// must demonstrate callbacks. The agent never edits the trust record.
		delete(settings, "disableAllHooks")
		enabled, _ := json.Marshal(settings)
		if err = os.WriteFile(path, enabled, 0600); err != nil {
			return err
		}
		j.record("Scripted user", "Enable hooks through Claude's native settings for the activation check.")
		return j.tui()
	}
	// Inspect the native warning in two separate processes. Declining trust is
	// intentionally not treated as a decline to install the existing definitions.
	for attempt := 1; attempt <= 2; attempt++ {
		ctx, cancel := context.WithTimeout(j.ctx, 20*time.Second)
		log := filepath.Join(j.home, fmt.Sprintf("trust-%d.log", attempt))
		cmd := exec.CommandContext(ctx, "python3", "-c", terminalDriver, j.c.Codex, "--no-alt-screen", "--sandbox", "workspace-write", "-c", `approval_policy="on-request"`, "-c", `approvals_reviewer="auto_review"`, "-C", j.repo)
		cmd.Dir = j.repo
		cmd.Env = append(j.env, "TERM=xterm-256color", "DOOMER_TEST_HARNESS=codex", "DOOMER_TEST_TRUST_HOOKS=deny", "DOOMER_TEST_HOOK_WARNING_ONLY=1", "DOOMER_TEST_TERMINAL_LOG="+log)
		input, err := cmd.StdinPipe()
		if err != nil {
			cancel()
			return err
		}
		out, err := cmd.CombinedOutput()
		input.Close()
		cancel()
		text := terminalEscapes.ReplaceAllString(string(out), "")
		j.record("Native hook trust", text)
		j.result.Checks[fmt.Sprintf("native_untrusted_hooks_prompt_%d", attempt)] = strings.Contains(text, "Hooks need review") && strings.Contains(text, "Scripted hook trust choice: deny")
		if err != nil || !j.result.Checks[fmt.Sprintf("native_untrusted_hooks_prompt_%d", attempt)] {
			return fmt.Errorf("native hook trust prompt missing on attempt %d: %v", attempt, err)
		}
	}
	return nil
}

func claudeDisabledHookReminder(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "hook") && (strings.Contains(message, "disabled") || strings.Contains(message, "enable") || strings.Contains(message, "turned off") || strings.Contains(message, "switched off"))
}
