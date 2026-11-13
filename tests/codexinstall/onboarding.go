package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

type installerState struct {
	Executable string                                    `json:"executable"`
	Runtime    string                                    `json:"runtime"`
	Agents     []string                                  `json:"agents"`
	Grants     []struct{ Scope, API, ApprovedAt string } `json:"grants"`
}

func (j *journey) command(directory, executable string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(j.ctx, executable, args...)
	cmd.Dir, cmd.Env = directory, j.env
	out, err := cmd.CombinedOutput()
	j.record("Fixture command", executable+" "+strings.Join(args, " ")+"\n"+string(out))
	if err != nil {
		return out, fmt.Errorf("fixture command %s: %w", filepath.Base(executable), err)
	}
	return out, nil
}

func (j *journey) state() (installerState, error) {
	var state installerState
	raw, err := os.ReadFile(filepath.Join(j.home, ".local/share/doomer-node/state.json"))
	if err == nil {
		err = json.Unmarshal(raw, &state)
	}
	return state, err
}

// The manager is the actual candidate package. Only its external skill source
// and skills-CLI version are pinned in this private copy; npm still executes the
// real skills CLI and the real manager installs the candidate Go executable.
func (j *journey) prepareManager() (string, error) {
	if j.c.NodeSource == "" || j.c.SkillsSource == "" || j.c.CandidateFile == "" || j.c.SkillsCLIVersion == "" || j.c.ToolingSource == "" {
		return "", fmt.Errorf("onboarding requires captured --node-source, --skills-source, skills CLI version and a candidate executable")
	}
	root := filepath.Join(filepath.Dir(j.home), "manager")
	if err := copyTree(j.c.NodeSource, root); err != nil {
		return "", err
	}
	path := filepath.Join(root, "src/setup.js")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(raw)
	const source = "'doomerlabs/skills#feat/doomer-node-skill'"
	if strings.Count(text, source) != 1 || strings.Count(text, "'--package=skills'") != 1 {
		return "", fmt.Errorf("manager skill installer changed; review fixture source pin")
	}
	text = strings.Replace(text, source, strconv.Quote(j.c.SkillsSource), 1)
	tooling := filepath.Join(filepath.Dir(j.home), "tooling")
	if err = copyTree(j.c.ToolingSource, tooling); err != nil {
		return "", err
	}
	if _, err = j.command(tooling, "npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"); err != nil {
		return "", err
	}
	text = strings.Replace(text, "'--package=skills'", strconv.Quote("--prefix="+tooling), 1)
	if err = os.WriteFile(path, []byte(text), 0600); err != nil {
		return "", err
	}
	j.record("Fixture inputs", "Pinned real skills CLI "+j.c.SkillsCLIVersion+" and captured candidate skill; no installer behavior mocked.")
	if _, err = j.command(root, "npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"); err != nil {
		return "", err
	}
	j.env = append(j.env, "DOOMER_NATIVE_BINARY="+j.c.CandidateFile)
	return filepath.Join(root, "bin/doomer.js"), nil
}

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(target, rel), 0700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular manager source: %s", rel)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(target, rel), raw, 0600|info.Mode().Perm()&0111)
	})
}

func (j *journey) onboarding() error {
	manager, err := j.prepareManager()
	if err != nil {
		return err
	}
	if j.c.Scenario == "skill-first" {
		// This is the public skills-first entry point, using the real pinned
		// skills CLI against the captured skill repository.
		if _, err = j.command(j.repo, "npm", "exec", "--yes", "--prefix="+filepath.Join(filepath.Dir(j.home), "tooling"), "--", "skills", "add", j.c.SkillsSource, "--skill", "doomer", "--global", "--yes", "--agent", skillsAgent(j.c)); err != nil {
			return err
		}
	}
	mode := "install"
	if j.c.Scenario == "skill-first" {
		mode = "onboard"
	}
	options := " --agent " + agentName(j.c)
	if j.c.Scenario == "standalone" || j.c.Scenario == "hooks-declined" {
		options = " --no-hooks"
	}
	consent := "I approve configuring the selected agent hooks and global source uploads to this isolated fixture service. You may pass --approve-uploads."
	if j.c.Scenario == "decline-uploads" {
		consent = "I approve installation and hooks, but decline all source uploads. Do not grant upload approval or pass --approve-uploads."
	} else {
		options += " --approve-uploads"
	}
	prompt := "Set up Doomer using this candidate Node manager: node " + strconv.Quote(manager) + " " + mode + options + " --json. " + consent + " This is a disposable test home; preserve the fixture API environment. Do not edit the repository, install a legacy skill, run a review, or build a CLI. Verify readiness with this manager's doctor --json."
	j.result.Turns++
	if _, err = runAgent(j.ctx, j.c, j.repo, j.env, prompt, "", j.record); err != nil {
		return err
	}
	state, err := j.state()
	if err != nil {
		return fmt.Errorf("Node manager did not persist installation: %w", err)
	}
	j.result.Checks["native_cli_installed"] = state.Executable == filepath.Join(j.home, ".local/bin/doomer")
	j.result.Checks["manager_installed"] = state.Runtime != ""
	doctorRaw, err := j.command(j.repo, "node", manager, "doctor", "--json")
	if err != nil {
		return err
	}
	var doctor struct {
		Ready bool `json:"ready"`
	}
	if err = json.Unmarshal(doctorRaw, &doctor); err != nil {
		return err
	}
	j.result.Checks["doctor_ready"] = doctor.Ready
	raw, err := os.ReadFile(filepath.Join(j.home, ".agents/skills/doomer/SKILL.md"))
	if err != nil {
		return err
	}
	j.result.Checks["new_skill_installed"] = strings.Contains(string(raw), "name: doomer")
	status, err := j.command(j.repo, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	j.result.Checks["no_repository_setup_files"] = len(status) == 0
	j.f.mu.Lock()
	j.result.Checks["onboarding_never_uploads_source"] = j.f.Uploads == 0 && len(j.f.Reviews) == 0
	j.f.mu.Unlock()
	if j.c.Scenario == "decline-uploads" {
		j.result.Checks["upload_refusal_preserved"] = len(state.Grants) == 0
		if err = j.terminalCoding("Create hello.py that prints hello. I still decline source uploads; do not grant permission or submit a manual review.", false); err != nil {
			return err
		}
		j.f.mu.Lock()
		j.result.Checks["declined_coding_never_uploads"] = j.f.Uploads == 0 && len(j.f.Reviews) == 0
		j.f.mu.Unlock()
		return nil
	}
	j.result.Checks["service_global_consent"] = len(state.Grants) == 1 && state.Grants[0].Scope == "global" && state.Grants[0].API == environmentValue(j.env, "DOOMER_API_URL")
	if j.c.Scenario == "standalone" || j.c.Scenario == "hooks-declined" {
		j.result.Checks["no_hook_selection"] = len(state.Agents) == 0
		if j.c.Scenario == "hooks-declined" {
			return nil
		}
		j.result.Turns++
		_, err = runAgent(j.ctx, j.c, j.repo, j.env, "Create hello.py that prints hello followed by a newline. Use the installed Go CLI for one standalone Quick review and report its result. Do not install hooks.", "", j.record)
		if err != nil {
			return err
		}
		return j.checkReviews(1, false)
	}
	j.result.Checks["correct_agent_selection"] = reflect.DeepEqual(state.Agents, []string{agentName(j.c)})
	if err = j.checkHookDefinitions(); err != nil {
		return err
	}
	switch j.c.Scenario {
	case "install", "skill-first":
		return nil
	case "repeat-onboarding":
		before, _ := os.ReadFile(agentHooksPath(j.c, j.home))
		if _, err = j.command(j.repo, "node", manager, "onboard", "--agent", agentName(j.c), "--json"); err != nil {
			return err
		}
		after, _ := os.ReadFile(agentHooksPath(j.c, j.home))
		again, err := j.state()
		if err != nil {
			return err
		}
		j.result.Checks["repeat_preserves_hooks"] = string(before) == string(after)
		j.result.Checks["repeat_reuses_consent"] = reflect.DeepEqual(again.Grants, state.Grants)
		return nil
	case "hook-repair":
		if err = os.WriteFile(agentHooksPath(j.c, j.home), []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo fixture-unrelated"}]}]}}`), 0600); err != nil {
			return err
		}
		if _, err = j.command(j.repo, "node", manager, "install-hooks", "--agent", agentName(j.c), "--json"); err != nil {
			return err
		}
		raw, _ := os.ReadFile(agentHooksPath(j.c, j.home))
		j.result.Checks["repair_preserves_unrelated_hook"] = strings.Contains(string(raw), "echo fixture-unrelated")
		return j.checkHookDefinitions()
	case "uninstall":
		if _, err = j.command(j.repo, "node", manager, "uninstall", "--json"); err != nil {
			return err
		}
		_, nativeErr := os.Lstat(state.Executable)
		_, stateErr := os.Stat(filepath.Join(j.home, ".local/share/doomer-node/state.json"))
		j.result.Checks["uninstall_removes_owned_command"] = os.IsNotExist(nativeErr)
		j.result.Checks["uninstall_removes_state"] = os.IsNotExist(stateErr)
		hooks, _ := os.ReadFile(agentHooksPath(j.c, j.home))
		j.result.Checks["uninstall_removes_owned_hooks"] = !strings.Contains(string(hooks), "--owner=doomer-node")
		return nil
	case "service-isolation":
		// A grant for one service never authorizes another endpoint.
		j.env = append(j.env, "DOOMER_API_URL=http://127.0.0.1:1")
		out, probeErr := j.command(j.repo, state.Executable, "has-permission", "--json")
		var permission struct {
			Approved bool `json:"approved"`
		}
		j.result.Checks["other_service_not_authorized"] = probeErr == nil && json.Unmarshal(out, &permission) == nil && !permission.Approved
		return nil
	case "nested-repositories":
		for _, name := range []string{"org/a", "org/nested/b"} {
			root := filepath.Join(j.repo, name)
			if err = os.MkdirAll(root, 0700); err != nil {
				return err
			}
			if _, err = j.command(root, "git", "init", "-q"); err != nil {
				return err
			}
			if _, err = j.command(root, "git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "Fixture"); err != nil {
				return err
			}
		}
		if err = j.terminalCoding("Create org/a/hello.py and org/nested/b/hello.py, each printing hello followed by a newline. Do not run Doomer review manually; let the installed hooks review the changes.", false); err != nil {
			return err
		}
		return j.checkReviews(2, true)
	case "read-only":
		if err = j.terminalCoding("Report the current branch and whether the working tree is clean. Do not edit files or submit reviews.", true); err != nil {
			return err
		}
		j.f.mu.Lock()
		defer j.f.mu.Unlock()
		j.result.Checks["read_only_no_upload"] = j.f.Uploads == 0 && len(j.f.Reviews) == 0
		return nil
	case "first-review", "new-chat":
		j.f.mu.Lock()
		j.f.Findings = true
		j.f.mu.Unlock()
		if err = j.terminalCoding("Create hello.py that prints hello followed by a newline. Do not run Doomer review manually; let the installed hooks review the changes and address any findings.", false); err != nil {
			return err
		}
		if err = j.checkReviews(1, true); err != nil {
			return err
		}
		if j.c.Scenario == "new-chat" {
			if err = j.terminalCoding("Create second.py that prints second followed by a newline. Do not run Doomer review manually; let the installed hooks review the changes and address any findings.", false); err != nil {
				return err
			}
			return j.checkReviews(2, true)
		}
		return nil
	}
	return fmt.Errorf("unhandled onboarding scenario %s", j.c.Scenario)
}

func skillsAgent(c config) string {
	if c.Harness == "claude" {
		return "claude-code"
	}
	return "codex"
}

func (j *journey) checkHookDefinitions() error {
	raw, err := os.ReadFile(agentHooksPath(j.c, j.home))
	if err != nil {
		return err
	}
	var config struct {
		Disabled bool `json:"disableAllHooks"`
		Hooks    map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err = json.Unmarshal(raw, &config); err != nil {
		return err
	}
	final := "Interrupt"
	if j.c.Harness == "claude" {
		final = "SessionEnd"
	}
	valid := !config.Disabled
	// The manager shell-quotes POSIX paths with single quotes.
	prefix := "'" + strings.ReplaceAll(filepath.Join(j.home, ".local/bin/doomer"), "'", "'\"'\"'") + "' watch agent-hook "
	for _, event := range []string{"UserPromptSubmit", "Stop", final} {
		owned := 0
		for _, group := range config.Hooks[event] {
			for _, hook := range group.Hooks {
				if strings.Contains(hook.Command, "--owner=doomer-node") {
					owned++
					valid = valid && strings.HasPrefix(hook.Command, prefix) && strings.Contains(hook.Command, "--agent="+agentName(j.c)) && strings.Contains(hook.Command, "--manager-home=")
				}
			}
		}
		valid = valid && owned == 1
	}
	j.result.Checks["direct_go_hooks"] = valid
	return nil
}

func environmentValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") {
			value = strings.TrimPrefix(entry, key+"=")
		}
	}
	return value
}

func (j *journey) checkReviews(minimum int, hooks bool) error {
	j.f.mu.Lock()
	defer j.f.mu.Unlock()
	groups := map[string]bool{}
	hookRequests := true
	for _, r := range j.f.Reviews {
		groups[r.GroupID] = true
		hookRequests = hookRequests && r.UserRequest != nil && r.UserRequest.SessionID != "" && r.UserRequest.TurnID != ""
	}
	j.result.Checks["review_received"] = len(j.f.Reviews) >= minimum
	if hooks {
		j.result.Checks["hook_session_identity"] = hookRequests
		j.result.Checks["distinct_review_groups"] = len(groups) >= minimum
	}
	if j.f.Findings {
		j.result.Checks["explicit_finding_feedback"] = len(j.f.Feedback) >= minimum
	}
	return nil
}
