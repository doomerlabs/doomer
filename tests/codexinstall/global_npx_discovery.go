package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/doomerlabs/doomer/internal/watch"
)

func installGlobalNpxSkill(ctx context.Context, home, repo string, env []string, record func(string, string)) error {
	for _, base := range []string{home, repo} {
		for _, agent := range []string{".agents", ".claude"} {
			if _, err := os.Stat(filepath.Join(base, agent, "skills/doomer-review")); !os.IsNotExist(err) {
				return fmt.Errorf("npx test requires an uninstalled skill at %s/%s", base, agent)
			}
		}
	}
	// Target both supported agents even when the installer detects a Codex host.
	args := []string{"--yes", "skills@1.5.18", "add", "doomerlabs/skills", "--skill", "doomer-review", "--global", "--agent", "codex", "claude-code", "--yes"}
	cmd := exec.CommandContext(ctx, "/usr/local/bin/npx", args...)
	cmd.Dir = repo
	cmd.Env = append(env, "CODEX_SANDBOX=seatbelt", "npm_config_cache="+filepath.Join(home, ".npm"), "PATH="+filepath.Join(home, ".local/bin")+":/usr/local/bin:/usr/bin:/bin")
	out, err := cmd.CombinedOutput()
	record("Installer", "Real npx global installation for Codex and Claude Code\n"+string(out))
	if err != nil {
		return fmt.Errorf("global npx installation: %w: %s", err, out)
	}
	originals := map[string][]byte{}
	for _, agent := range []string{".agents", ".claude"} {
		path := filepath.Join(home, agent, "skills/doomer-review/SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		originals[path] = data
	}
	snapshot, err := json.Marshal(originals)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "npx-skill-snapshot.json"), snapshot, 0600)
}

func (j *journey) globalNpxDiscovery() error {
	originals := map[string][]byte{}
	snapshot, err := os.ReadFile(filepath.Join(j.home, "npx-skill-snapshot.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(snapshot, &originals); err != nil {
		return err
	}
	for _, agent := range []string{".agents", ".claude"} {
		path := filepath.Join(j.home, agent, "skills/doomer-review/SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, originals[path]) {
			return fmt.Errorf("onboarding changed npx-owned skill %s", path)
		}
		if _, err := os.Stat(filepath.Join(j.repo, agent, "skills/doomer-review")); !os.IsNotExist(err) {
			return fmt.Errorf("global install wrote a repository skill")
		}
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(j.repo, name)); !os.IsNotExist(err) {
			return fmt.Errorf("repository discovery fallback exists: %s", name)
		}
	}
	if err := watch.Install(agentHooksPath(j.c, j.home), "", "--managed-task-adapter", true); err != nil {
		return err
	}
	j.result.Checks["real_global_npx_install"] = true
	j.result.Checks["no_repository_skill_or_instructions"] = true
	j.result.Checks["discovery_hooks_disabled"] = true
	if err := j.tui(); err != nil {
		return err
	}
	for path, original := range originals {
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, original) {
			return fmt.Errorf("agent changed npx-owned skill %s", path)
		}
	}
	j.result.Checks["npx_skill_preserved"] = true
	tasks, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return fmt.Errorf("no saved task proves personal skill invocation")
	}
	for _, task := range tasks {
		if task.Bootstrap.Scope != "personal" {
			return fmt.Errorf("task did not discover personal skill")
		}
	}
	j.result.Checks["personal_skill_invoked_in_tui"] = true
	return nil
}
