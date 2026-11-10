package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/doomerlabs/doomer/internal/watch"
)

func installGlobalNpxSkill(ctx context.Context, home, repo, source string, env []string, record func(string, string)) error {
	for _, base := range []string{home, repo} {
		for _, agent := range []string{".agents", ".claude"} {
			if _, err := os.Stat(filepath.Join(base, agent, "skills/doomer-review")); !os.IsNotExist(err) {
				return fmt.Errorf("npx test requires an uninstalled skill at %s/%s", base, agent)
			}
		}
	}
	// Target both supported agents even when the installer detects a Codex host.
	args := []string{"--yes", "skills@1.5.18", "add", source, "--skill", "doomer-review", "--global", "--agent", "codex", "claude-code", "--yes"}
	npx, err := exec.LookPath("npx")
	if err != nil {
		return fmt.Errorf("global npx discovery requires Node.js and npm: %w", err)
	}
	cmd := exec.CommandContext(ctx, npx, args...)
	cmd.Dir = repo
	cmd.Env = append(env, "CODEX_SANDBOX=seatbelt", "npm_config_cache="+filepath.Join(home, ".npm"), "PATH="+filepath.Join(home, ".local/bin")+":"+filepath.Dir(npx)+":/usr/local/bin:/usr/bin:/bin")
	out, err := cmd.CombinedOutput()
	record("Installer", "Real npx global installation for Codex and Claude Code\n"+string(out))
	if err != nil {
		return fmt.Errorf("global npx installation: %w: %s", err, out)
	}
	snapshot, err := snapshotNpxInstall(home)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "npx-skill-snapshot.json"), snapshot, 0600)
}

func (j *journey) globalNpxDiscovery() error {
	snapshot, err := os.ReadFile(filepath.Join(j.home, "npx-skill-snapshot.json"))
	if err != nil {
		return err
	}
	current, err := snapshotNpxInstall(j.home)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, snapshot) {
		return fmt.Errorf("onboarding changed npx-owned installation")
	}
	for _, agent := range []string{".agents", ".claude"} {
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
	setupTasks, err := savedTasks(j.home)
	if err != nil || len(setupTasks) != 1 {
		return fmt.Errorf("expected one setup task before npx discovery: %v", err)
	}
	decline := exec.CommandContext(j.ctx, filepath.Join(j.home, ".local/bin/doomer"), "task", "hook-setup", setupTasks[0].Handle, "--decline")
	decline.Env = j.env
	if out, err := decline.CombinedOutput(); err != nil {
		return fmt.Errorf("record optional-hook decline: %w: %s", err, out)
	}
	j.result.Checks["real_global_npx_install"] = true
	j.result.Checks["no_repository_skill_or_instructions"] = true
	j.result.Checks["discovery_hooks_disabled"] = true
	if err := j.tui(); err != nil {
		return err
	}
	current, err = snapshotNpxInstall(j.home)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, snapshot) {
		return fmt.Errorf("agent changed npx-owned installation")
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

// Capture only installer-owned roots, including links and package metadata.
func snapshotNpxInstall(home string) ([]byte, error) {
	type entry struct {
		Mode   fs.FileMode
		Target string
		Data   []byte
	}
	entries := map[string]entry{}
	for _, root := range []string{".agents/skills/doomer-review", ".claude/skills/doomer-review", ".agents/.skill-lock.json"} {
		err := filepath.WalkDir(filepath.Join(home, root), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			value := entry{Mode: info.Mode()}
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				value.Target, err = os.Readlink(path)
			case info.Mode().IsRegular():
				value.Data, err = os.ReadFile(path)
			case info.IsDir():
			default:
				return fmt.Errorf("unsupported npx package entry %s", path)
			}
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(home, path)
			if err != nil {
				return err
			}
			entries[relative] = value
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(entries)
}
