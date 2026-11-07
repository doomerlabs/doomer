package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
	"go.yaml.in/yaml/v3"
)

func (j *journey) upgrade() error {
	base := j.repo
	if personalScenario(j.c.Scenario) {
		base = j.home
	}
	initial, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	initialHandles := map[string]bool{}
	for _, task := range initial {
		initialHandles[task.Handle] = true
	}
	// Prepare a genuine older signed runtime and an upstream historical bootstrap.
	updater := selfupdate.Updater{Home: j.home, Versioned: true}
	if _, err := updater.Apply(j.ctx, "2026.11.6.9", false); err != nil {
		return err
	}

	metadata, err := yaml.Marshal(map[string]string{"doomer-protocol": "2", "doomer-update-policy": "stable", "doomer-bootstrap-version": "2026.11.6.10", "doomer-cli-range": ">=2026.11.6.9"})
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(metadata)), "\n")
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	old := strings.Replace(j.c.LegacyBootstrap, "{{metadata}}", strings.Join(lines, "\n"), 1)
	if j.c.Scenario == "upgrade-custom" {
		old += "\nKeep the user custom note intact.\n"
	}
	for _, agent := range []string{".agents", ".claude"} {
		if err = os.WriteFile(filepath.Join(base, agent, "skills/doomer-review/SKILL.md"), []byte(old), 0600); err != nil {
			return err
		}
	}
	if err = j.git(j.repo, "branch", "-M", "main"); err != nil {
		return err
	}
	if j.c.Scenario != "upgrade-untracked" {
		if err = j.git(j.repo, "add", "."); err != nil {
			return err
		}
		if err = j.git(j.repo, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "Historical bootstrap fixture"); err != nil {
			return err
		}
	}
	remote := filepath.Join(filepath.Dir(j.repo), "origin.git")
	if err = j.git(j.repo, "init", "--bare", remote); err != nil {
		return err
	}
	if err = j.git(j.repo, "remote", "add", "origin", remote); err != nil {
		return err
	}
	if err = j.git(j.repo, "push", "-u", "origin", "main"); err != nil {
		return err
	}
	bin := filepath.Join(j.home, "fixture-bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		return err
	}
	marker := filepath.Join(j.home, "pr-created")
	oldLauncher := "#!/bin/sh\nexec '" + runtimeversion.Binary(j.home, "2026.11.6.9") + "' \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "doomer"), []byte(oldLauncher), 0700); err != nil {
		return err
	}
	shim := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\n *--help*) echo 'Usage: gh pr create [flags]'; exit 0 ;;\nesac\ncase \"$1 $2\" in\n 'pr create') printf 'created\\n' >> '%s'; echo https://example.invalid/pull/1 ;;\n 'pr list') echo '[]' ;;\n 'repo view') echo '{\"defaultBranchRef\":{\"name\":\"main\"}}' ;;\n 'auth status') exit 0 ;;\n *) exit 0 ;;\nesac\n", marker)
	if err = os.WriteFile(filepath.Join(bin, "gh"), []byte(shim), 0700); err != nil {
		return err
	}
	for i, value := range j.env {
		if strings.HasPrefix(value, "PATH=") {
			j.env[i] = "PATH=" + bin + ":" + strings.TrimPrefix(value, "PATH=")
		}
	}
	j.result.Turns++
	turn, err := runAgent(j.ctx, j.c, j.repo, j.env, "Use the installed Doomer skill to prepare a new task and check for an available skill update. Do not apply the skill update yet. Keep the current update policy and reuse existing grants.", "", j.record)
	if err != nil {
		return err
	}
	before, err := os.ReadFile(filepath.Join(base, ".agents/skills/doomer-review/SKILL.md"))
	if err != nil {
		return err
	}
	if string(before) != old {
		return fmt.Errorf("skill updated before acceptance")
	}
	if j.c.Scenario == "upgrade-custom" {
		j.result.Turns++
		if _, err = runAgent(j.ctx, j.c, j.repo, j.env, "Preserve the customized skill exactly as it is. Use no automated upgrade and finish this inspection.", turn.Session, j.record); err != nil {
			return err
		}
		after, err := os.ReadFile(filepath.Join(base, ".agents/skills/doomer-review/SKILL.md"))
		if err != nil || string(after) != old {
			return fmt.Errorf("customized skill was overwritten")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			return fmt.Errorf("customized skill unexpectedly produced a PR")
		}
		j.result.Checks["custom_skill_preserved"] = true
		return nil
	}
	if j.c.Scenario == "upgrade-defer" {
		j.result.Turns++
		if _, err = runAgent(j.ctx, j.c, j.repo, j.env, "Maybe later. Keep the current skill and finish this task.", turn.Session, j.record); err != nil {
			return err
		}
		raw, err := os.ReadFile(filepath.Join(base, ".agents/skills/doomer-review/SKILL.md"))
		if err != nil || string(raw) != old {
			return fmt.Errorf("deferred upgrade changed the skill")
		}
		cmd := exec.CommandContext(j.ctx, filepath.Join(j.home, ".local/bin/doomer"), "prepare", "--agent", agentName(j.c), "--repo", j.repo, "--prompt", "Check readiness after deferring the update")
		cmd.Env = j.env
		output, err := cmd.Output()
		if err != nil {
			return err
		}
		var status struct {
			Updates []struct {
				State string `json:"state"`
			} `json:"bootstrap_updates"`
		}
		if err = json.Unmarshal(output, &status); err != nil {
			return err
		}
		for _, update := range status.Updates {
			if update.State == "update_available" {
				return fmt.Errorf("deferred skill upgrade was offered again on the next preparation")
			}
		}
		j.result.Checks["deferred_upgrade_stays_quiet"] = true
		j.f.mu.Lock()
		j.result.Checks["grant_reused"] = j.f.Creates == 1
		j.f.mu.Unlock()
		if !j.result.Checks["grant_reused"] {
			return fmt.Errorf("deferring the upgrade created another grant")
		}
		return nil
	}
	j.result.Turns++
	_, err = runAgent(j.ctx, j.c, j.repo, j.env, "Update skill. I accept the available update. Complete its normal repository PR workflow, preserving the current update policy.", turn.Session, j.record)
	if err != nil {
		return err
	}
	after, err := os.ReadFile(filepath.Join(base, ".agents/skills/doomer-review/SKILL.md"))
	if err != nil {
		return err
	}
	created, err := os.ReadFile(marker)
	expectsPR := j.c.Scenario != "upgrade-untracked" && !personalScenario(j.c.Scenario)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if expectsPR && os.IsNotExist(err) {
		return fmt.Errorf("upgrade did not create a PR")
	}
	j.result.Checks["skill_changed_after_acceptance"] = string(after) != old
	j.result.Checks["stable_policy_preserved"] = strings.Contains(string(after), "doomer-update-policy: stable")
	if expectsPR {
		j.result.Checks["one_upgrade_pr"] = strings.Count(string(created), "created") == 1
	} else {
		j.result.Checks["no_upgrade_pr"] = len(created) == 0
	}
	tasks, err := savedTasks(j.home)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if !initialHandles[task.Handle] && task.Release != "" && task.Release != "2026.11.6.9" {
			j.result.Checks["new_runtime_selected"] = true
		}
	}
	if !j.result.Checks["new_runtime_selected"] {
		return fmt.Errorf("older launcher did not select current runtime")
	}
	j.f.mu.Lock()
	j.result.Checks["grant_reused"] = j.f.Creates == 1
	j.f.mu.Unlock()
	for _, ok := range j.result.Checks {
		if !ok {
			return fmt.Errorf("upgrade assertion failed")
		}
	}
	return nil
}
