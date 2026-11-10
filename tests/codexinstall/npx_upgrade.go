package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/doomerlabs/doomer/internal/watch"
)

func (j *journey) npxSkillUpgrade() error {
	initial, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	prior := map[string]bool{}
	for _, task := range initial {
		prior[task.Handle] = true
	}
	// Reproduce the global skills installer's files and lock without depending on
	// a mutable npm release or changing the published skill during the test.
	canonical := filepath.Join(j.home, ".agents/skills/doomer-review")
	linked := filepath.Join(j.home, ".claude/skills/doomer-review")
	if err := os.MkdirAll(filepath.Join(canonical, "agents"), 0700); err != nil {
		return err
	}
	if j.c.PersonalUpgradeBootstrap == "" {
		return fmt.Errorf("missing historical personal bootstrap fixture")
	}
	if err := os.WriteFile(filepath.Join(canonical, "SKILL.md"), []byte(j.c.PersonalUpgradeBootstrap), 0600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(canonical, "agents/openai.yaml"), []byte("policy:\n  allow_implicit_invocation: true\n"), 0600); err != nil {
		return err
	}
	if err := os.RemoveAll(linked); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(linked), 0700); err != nil {
		return err
	}
	if err := os.Symlink("../../.agents/skills/doomer-review", linked); err != nil {
		return err
	}
	lockPath := filepath.Join(j.home, ".agents/.skill-lock.json")
	lock := []byte(`{"version":3,"skills":{"doomer-review":{"source":"doomerlabs/skills","sourceType":"github","sourceUrl":"https://github.com/doomerlabs/skills","skillPath":"skills/doomer-review/SKILL.md","skillFolderHash":"fixture"}}}`)
	if err := os.WriteFile(lockPath, lock, 0600); err != nil {
		return err
	}
	bin := filepath.Join(j.home, "fixture-bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		return err
	}
	marker := filepath.Join(j.home, "npx-executed")
	trap := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexit 1\n", marker)
	if err := os.WriteFile(filepath.Join(bin, "npx"), []byte(trap), 0700); err != nil {
		return err
	}
	for i, value := range j.env {
		if strings.HasPrefix(value, "PATH=") {
			j.env[i] = "PATH=" + bin + ":" + strings.TrimPrefix(value, "PATH=")
		}
	}
	// Disable hooks so the ordinary coding turn must actually discover the skill.
	if err := watch.Install(agentHooksPath(j.c, j.home), "", "--managed-task-adapter", true); err != nil {
		return err
	}
	for _, task := range initial {
		decline := exec.CommandContext(j.ctx, filepath.Join(j.home, ".local/bin/doomer"), "task", "hook-setup", task.Handle, "--decline")
		decline.Env = j.env
		if out, err := decline.CombinedOutput(); err != nil {
			return fmt.Errorf("fixture hook decline: %w: %s", err, out)
		}
	}
	personal := j.c.Scenario == "upgrade-personal-npx"
	prMention := regexp.MustCompile(`(?i)\b(?:pull request|PR)\b`)
	prQuestion := regexp.MustCompile(`(?is)\b(?:would you like|do you want|may I|can I|shall I|should I)\b.{0,120}\b(?:pull request|PR)\b`)
	loaded, accepted := false, false
	var messages []string
	record := j.record
	j.record = func(kind, text string) {
		record(kind, text)
		if kind == "Scripted user" && strings.HasPrefix(text, "Update skill.") {
			accepted = true
		}
		if kind == "Command" && ((strings.Contains(text, "# Doomer review") && strings.Contains(text, "At task start")) || (strings.HasPrefix(text, "Skill ") && strings.Contains(text, "doomer-review"))) {
			loaded = true
		}
		if (kind == "Codex" || kind == "Claude" || kind == "User question") && !accepted {
			messages = append(messages, text)
		}
	}
	defer func() { j.record = record }()
	if personal {
		if err := j.coding(j.repo, "Create hello.py that prints hello followed by a newline when run with Python 3."); err != nil {
			return err
		}
		visible := strings.Join(messages, "\n")
		j.result.Checks["personal_skill_loaded_without_reminder"] = loaded
		j.result.Checks["manual_npx_update_message"] = strings.Contains(visible, "npx skills update doomer-review --global")
		j.result.Checks["no_personal_update_pr"] = !prQuestion.MatchString(visible)
		command := exec.CommandContext(j.ctx, "python3", filepath.Join(j.repo, "hello.py"))
		out, err := command.Output()
		if err != nil || string(out) != "hello\n" {
			return fmt.Errorf("ordinary source task failed: %s (%v)", out, err)
		}
	} else {
		if err := j.upgrade(); err != nil {
			return err
		}
		visible := strings.ToLower(strings.Join(messages, "\n"))
		j.result.Checks["repository_pr_requested_before_acceptance"] = prMention.MatchString(visible)
		j.result.Checks["repository_uses_native_update"] = !strings.Contains(visible, "npx skills update")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		return fmt.Errorf("agent executed the user's manual npx update command")
	}
	j.result.Checks["npx_update_not_executed"] = true
	for path, want := range map[string][]byte{
		filepath.Join(canonical, "SKILL.md"): []byte(j.c.PersonalUpgradeBootstrap),
		lockPath:                             lock,
	} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			return fmt.Errorf("agent changed installer-owned file %s: %v", path, err)
		}
	}
	if target, err := os.Readlink(linked); err != nil || target != "../../.agents/skills/doomer-review" {
		return fmt.Errorf("agent changed installer-owned symlink: %v", err)
	}
	j.result.Checks["personal_install_preserved"] = true
	tasks, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	scope, base := "project", j.repo
	if personal {
		scope, base = "personal", j.home
	}
	for _, task := range tasks {
		if prior[task.Handle] {
			continue
		}
		if task.Bootstrap.Scope != scope || !strings.HasPrefix(task.Bootstrap.Path, base+string(filepath.Separator)) {
			return fmt.Errorf("task used the wrong skill scope: %+v", task.Bootstrap)
		}
		// A prepare attempt can stop at update approval before selecting a runtime.
		// Only that pre-runtime state may lack a release, never a prepared task.
		if task.Release == "" && (task.Stage != "preparing" || task.Submission != nil || task.Review != nil || task.InstallationID != "") {
			return fmt.Errorf("task reached stage %s without a candidate runtime", task.Stage)
		}
		if task.Release != "" {
			if j.c.CandidateVersion != "" && task.Release != j.c.CandidateVersion {
				return fmt.Errorf("task used release %s instead of candidate %s", task.Release, j.c.CandidateVersion)
			}
			j.result.Checks["candidate_runtime_used"] = true
		}
		j.result.Checks["correct_skill_precedence"] = true
	}
	if !j.result.Checks["candidate_runtime_used"] {
		return fmt.Errorf("no prepared task used the candidate runtime")
	}
	if personal {
		return j.finish()
	}
	for _, ok := range j.result.Checks {
		if !ok {
			return fmt.Errorf("repository precedence assertion failed")
		}
	}
	return nil
}
