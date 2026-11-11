package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"go.yaml.in/yaml/v3"
)

// Exercise the candidate's real installer after the verified public bootstrap.
// All paths belong to this run's disposable home and empty fixture repository.
func reinstallDiscoverySkills(ctx context.Context, c config, home, repo string, env []string, record func(string, string)) error {
	for _, base := range []string{home, repo} {
		for _, agent := range []string{".agents", ".claude"} {
			if err := os.RemoveAll(filepath.Join(base, agent, "skills/doomer-review")); err != nil {
				return err
			}
		}
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if err := os.Remove(filepath.Join(repo, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	args := []string{"install", "--repo", repo}
	if personalScenario(c.Scenario) {
		args = []string{"install", "--personal"}
	}
	cmd := exec.CommandContext(ctx, filepath.Join(home, ".local/bin/doomer"), args...)
	cmd.Dir, cmd.Env = repo, env
	out, err := cmd.CombinedOutput()
	record("fixture", "Run candidate installer: "+strings.Join(args, " ")+"\n"+string(out))
	if err != nil {
		return fmt.Errorf("candidate discovery installation: %w: %s", err, out)
	}
	return nil
}

func (j *journey) skillDiscovery() error {
	personal := personalScenario(j.c.Scenario)
	base, other, scope := j.repo, j.home, "project"
	if personal {
		base, other, scope = j.home, j.repo, "personal"
	}
	for _, agent := range []string{".agents", ".claude"} {
		path := filepath.Join(base, agent, "skills/doomer-review/SKILL.md")
		bootstrap, err := managed.ReadBootstrap(path, scope)
		if err != nil {
			return err
		}
		want, err := managed.BootstrapTextForScope(bootstrap.Policy, scope)
		if err != nil {
			return err
		}
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != want {
			return fmt.Errorf("installed %s description or instructions differ from the %s bootstrap", agent, scope)
		}
		if _, err := os.Stat(filepath.Join(other, agent, "skills/doomer-review")); !os.IsNotExist(err) {
			return fmt.Errorf("skill leaked outside %s installation scope", scope)
		}
		configPath := filepath.Join(base, agent, "skills/doomer-review/agents/openai.yaml")
		config, err := os.ReadFile(configPath)
		if personal && agent == ".agents" {
			var metadata struct {
				Policy struct {
					AllowImplicitInvocation bool `yaml:"allow_implicit_invocation"`
				}
			}
			if err != nil || yaml.Unmarshal(config, &metadata) != nil || !metadata.Policy.AllowImplicitInvocation {
				return fmt.Errorf("personal Codex invocation metadata missing or disabled")
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("personal Codex metadata leaked into %s/%s", scope, agent)
		}
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(j.repo, name))
		if personal {
			if !os.IsNotExist(err) {
				return fmt.Errorf("personal install wrote repository instructions: %s", name)
			}
		} else if err != nil || !bytes.Contains(data, []byte("skills/doomer-review/SKILL.md")) {
			return fmt.Errorf("repository installation lost discovery instructions: %s", name)
		}
		if _, err := os.Stat(filepath.Join(j.home, name)); !os.IsNotExist(err) {
			return fmt.Errorf("installation wrote global instructions: %s", name)
		}
	}
	j.result.Checks["skill_and_metadata_scoped"] = true
	// Remove the hook fallback so only skill discovery can initiate the review.
	if err := watch.Install(agentHooksPath(j.c, j.home), "", "--managed-task-adapter", true); err != nil {
		return err
	}
	tasks, err := savedTasks(j.home)
	if err != nil || len(tasks) != 1 {
		return fmt.Errorf("expected one setup task before discovery: %v", err)
	}
	decline := exec.CommandContext(j.ctx, filepath.Join(j.home, ".local/bin/doomer"), "task", "hook-setup", tasks[0].Handle, "--decline")
	decline.Env = j.env
	if out, err := decline.CombinedOutput(); err != nil {
		return fmt.Errorf("fixture hook decline: %w: %s", err, out)
	}
	j.result.Checks["discovery_hooks_disabled"] = true
	j.record("fixture", "Fresh agent session with hooks removed; the coding prompt contains no Doomer or skill reminder.")
	loaded := false
	record := j.record
	j.record = func(kind, text string) {
		record(kind, text)
		if loadedDoomerSkill(kind, text) {
			loaded = true
		}
	}
	err = j.coding(j.repo, "Create hello.py that prints hello followed by a newline when run with Python 3.")
	j.record = record
	if err != nil {
		return err
	}
	if !loaded {
		return fmt.Errorf("fresh agent did not load the installed %s skill", scope)
	}
	cmd := exec.CommandContext(j.ctx, "python3", filepath.Join(j.repo, "hello.py"))
	cmd.Env = j.env
	out, err := cmd.Output()
	if err != nil || string(out) != "hello\n" {
		return fmt.Errorf("ordinary coding task failed: %s (%v)", out, err)
	}
	j.result.Checks["skill_loaded_without_reminder"] = true
	j.result.Checks["ordinary_source_task_reviewed"] = true
	return j.finish()
}

// Identify the skill by its metadata and heading, not changeable instruction prose.
func loadedDoomerSkill(kind, text string) bool {
	return kind == "Command" && ((strings.Contains(text, "# Doomer review") && strings.Contains(text, "name: doomer-review")) || (strings.HasPrefix(text, "Skill ") && strings.Contains(text, "doomer-review")))
}
