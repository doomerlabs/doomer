package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func doctorAutoSetupChecks(c *cobra.Command, root, manager, agent string) []string {
	if root == "" || manager == "" || (agent != "claude" && agent != "codex") {
		return nil
	}
	state, err := readNPMSetupState(manager)
	if err != nil {
		return nil
	}
	var executable string
	binary := "setup_binary_unavailable"
	if state.get("executable", &executable) == nil && filepath.IsAbs(executable) {
		if _, err := exec.LookPath(executable); err == nil {
			current, currentErr := os.Stat(mustExecutable())
			configured, configuredErr := os.Stat(executable)
			if currentErr == nil && configuredErr == nil && os.SameFile(current, configured) {
				binary = "setup_binary_ready"
			}
		}
	}
	skill := "setup_skill_unknown"
	home, err := os.UserHomeDir()
	if err == nil {
		profile := filepath.Dir(watching.HookFile(home, agent))
		dirs := []string{filepath.Join(profile, "skills", "doomer")}
		if agent == "codex" {
			dirs = append(dirs, filepath.Join(home, ".agents", "skills", "doomer"))
		}
		skill = "setup_skill_unavailable"
		for _, dir := range dirs {
			if doctorReadableSkill(dir) {
				skill = "setup_skill_ready"
				break
			}
		}
	}

	consent, stateErr := state.consent()
	permission := doctorAutoReviewerPermission(home, agent, state, consent, executable)
	auth, upload := "setup_auth_unknown", "setup_upload_unknown"
	env, envErr := directoryWatchEnvironment(root)
	if stateErr == nil && envErr == nil {
		if api, err := agentHookAPI("", env.get("DOOMER_API_URL"), consent.API); err == nil {
			upload = "setup_upload_missing"
			if _, err := agentHookConsent(consent, api, agent, root); err == nil {
				upload = "setup_upload_ready"
			}
			auth = "setup_auth_unavailable"
			if token, err := watchToken(c, api, consent, env); err == nil {
				if adversarylabs.IsAnonymousToken(token) {
					auth = "setup_auth_anonymous"
				} else {
					auth = "setup_auth_unknown"
				}
				// A checkout can override its service. Verify credentials remotely only
				// when they came from the installed profile for its configured service.
				if auth == "setup_auth_unknown" && api == strings.TrimRight(consent.API, "/") && env.get("DOOMER_TOKEN") == "" {
					ctx, cancel := context.WithTimeout(c.Context(), 4*time.Second)
					account, err := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, api).Whoami(ctx, token)
					cancel()
					if err == nil && account.ID != "" {
						auth = "setup_auth_ready"
					} else if errors.Is(err, adversarylabs.ErrNotAuthenticated) {
						auth = "setup_auth_unavailable"
					}
				}
			}
		}
	}
	return []string{binary, skill, permission, auth, upload}
}

func doctorReadableSkill(dir string) bool {
	file := filepath.Join(dir, "SKILL.md")
	info, err := os.Stat(file)
	return err == nil && info.Mode().IsRegular() && info.Size() <= 1<<20 && npmSkillExists(file)
}

// Restore only a missing Claude profile entry previously recorded by the
// installer. Codex can already load the shared copy without a profile link.
func restoreDoctorSkill(ctx context.Context, manager string, target doctorAutoTarget) (string, error) {
	if target.agent != "claude" {
		return "", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	source := filepath.Join(home, ".agents", "skills", "doomer")
	destination := filepath.Join(filepath.Dir(target.file), "skills", "doomer")
	unlock, err := npmSetupLock(filepath.Join(manager, "state.json"))
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := os.Lstat(filepath.Join(manager, "uninstalling.json")); !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("personal Doomer installer is unavailable")
	}
	state, err := readNPMSetupState(manager)
	if err != nil {
		return "", err
	}
	var paths []string
	var hookFiles []string
	if err := state.get("skillPaths", &paths); err != nil {
		return "", err
	}
	if err := state.get("hookFiles", &hookFiles); err != nil {
		return "", err
	}
	if !slices.Contains(hookFiles, target.file) || !slices.Contains(paths, source) || !slices.Contains(paths, destination) || !doctorReadableSkill(source) {
		return "", nil
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	dir := filepath.Dir(destination)
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("the Claude skill directory is not a directory")
	}
	if err := os.Symlink(source, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return "", nil
		}
		return "", err
	}
	if !doctorReadableSkill(destination) {
		return "setup_skill_restore_unverified", fmt.Errorf("Doctor created the Claude skill link but could not read it")
	}
	return "setup_skill_restored", nil
}

func repairDoctorClaudePermission(c *cobra.Command, manager string, target doctorAutoTarget, changes *doctorRepairChanges) (string, error) {
	settings := npmSettings{files: map[string][]string{"claude": {target.file}}}
	options := npmSetupOptions{mode: "doctor", home: manager, executable: target.executable, claudeDecision: "allow"}
	if err := settings.claudePermission(c, options, npmSetupPrompt{}, changes); err != nil {
		return "setup_agent_permissions_repair_failed", err
	}
	state, err := readNPMSetupState(manager)
	if err != nil {
		return "setup_agent_permissions_repair_failed", err
	}
	consent, err := state.consent()
	if err != nil {
		return "setup_agent_permissions_repair_failed", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "setup_agent_permissions_repair_failed", err
	}
	if doctorAutoReviewerPermission(home, "claude", state, consent, target.executable) != "setup_agent_permissions_recorded" {
		return "setup_agent_permissions_repair_failed", fmt.Errorf("could not verify the Claude reviewer rules after saving")
	}
	return "setup_agent_permissions_repaired", nil
}

func doctorAutoReviewerPermission(home, agent string, state npmSetupState, consent agentHookState, executable string) string {
	if home == "" {
		return "setup_agent_permissions_unknown"
	}
	file := watching.HookFile(home, agent)
	if agent == "codex" {
		config := filepath.Join(filepath.Dir(file), "config.toml")
		data, err := readDoctorCodexConfig(config)
		if err != nil {
			return "setup_agent_permissions_unknown"
		}
		policy, err := codexExtraPolicy(data)
		if err != nil {
			return "setup_agent_permissions_unknown"
		}
		for _, saved := range consent.CodexPolicies {
			if saved.ConfigPath == config && saved.ExtraPolicy != "" {
				if strings.Contains(policy, saved.ExtraPolicy) {
					return "setup_agent_permissions_recorded"
				}
			}
		}
		if slices.ContainsFunc(consent.CodexPolicies, func(saved nodeCodexPolicy) bool {
			return saved.ConfigPath == config && saved.ExtraPolicy != ""
		}) {
			return "setup_agent_permissions_missing"
		}
		return "setup_agent_permissions_unknown"
	}
	var saved npmClaudePermission
	if state.get("claudeReviewPermission", &saved) != nil {
		return "setup_agent_permissions_unknown"
	}
	if saved.Decision == "decline" && (saved.File == "" || saved.File == file) {
		return "setup_agent_permissions_denied"
	}
	config, _, err := readDoctorAutoHookSettings(file)
	if err != nil {
		return "setup_agent_permissions_unknown"
	}
	block, _ := config["permissions"].(npmJSONObject)
	allow, _ := block["allow"].([]any)
	deny, _ := block["deny"].([]any)
	autoMode, _ := config["autoMode"].(npmJSONObject)
	autoAllow, _ := autoMode["allow"].([]any)
	rules, autoRules, err := npmClaudeRules([]string{executable})
	if err != nil {
		return "setup_agent_permissions_unknown"
	}
	for _, rule := range rules {
		if slices.Contains(deny, any(rule)) || slices.Contains(deny, any("Bash")) ||
			slices.Contains(deny, any("Bash(*)")) || slices.Contains(deny, any("Bash("+executable+" *)")) {
			return "setup_agent_permissions_denied"
		}
	}
	if slices.ContainsFunc(rules, func(rule string) bool { return !slices.Contains(allow, any(rule)) }) ||
		slices.ContainsFunc(autoRules, func(rule string) bool { return !slices.Contains(autoAllow, any(rule)) }) {
		if saved.Decision == "allow" && saved.File == file {
			return "setup_agent_permissions_missing"
		}
		if saved.Decision == "" {
			if slices.ContainsFunc(rules, func(rule string) bool { return slices.Contains(allow, any(rule)) }) ||
				slices.ContainsFunc(autoRules, func(rule string) bool { return slices.Contains(autoAllow, any(rule)) }) {
				return "setup_agent_permissions_incomplete"
			}
			return "setup_agent_permissions_no_personal_rules"
		}
		return "setup_agent_permissions_unknown"
	}
	return "setup_agent_permissions_recorded"
}

func printDoctorAutoChecks(c *cobra.Command, checks []string) {
	messages := map[string]string{
		"setup_binary_ready":                        "Executable: configured Doomer CLI matches and can run.",
		"setup_binary_unavailable":                  "Executable: configured Doomer CLI is missing, inaccessible, or points to another installation.",
		"setup_skill_restored":                      "Skill: restored and verified the link to the installed Doomer skill.",
		"setup_skill_restore_unverified":            "Skill: restored the link, but could not verify the Doomer skill.",
		"setup_skill_restore_failed":                "Skill: Doctor could not restore the Doomer skill link.",
		"setup_skill_ready":                         "Skill: found a readable Doomer skill for this agent profile.",
		"setup_skill_unavailable":                   "Skill: Doomer skill is missing or unreadable for this agent profile.",
		"setup_skill_unknown":                       "Skill: could not resolve the skill location.",
		"setup_agent_permissions_repaired":          "Agent permissions: restored and verified the scoped Doomer review rules.",
		"setup_agent_permissions_repair_failed":     "Agent permissions: Doctor could not complete or verify the repair.",
		"setup_agent_permissions_recorded":          "Agent permissions: found Doomer review rules in the agent settings.",
		"setup_agent_permissions_denied":            "Agent permissions: Doomer review commands were declined or denied.",
		"setup_agent_permissions_missing":           "Agent permissions: previously approved Doomer rules are missing.",
		"setup_agent_permissions_incomplete":        "Agent permissions: this Claude profile is missing some scoped Doomer review rules.",
		"setup_agent_permissions_no_personal_rules": "Agent permissions: this personal Claude profile has no matching Doomer allow rules or recorded reviewer choice.",
		"setup_agent_permissions_unknown":           "Agent permissions: could not verify this profile's Doomer review permissions.",
		"setup_auth_ready":                          "Authentication: account accepted by the configured service.",
		"setup_auth_anonymous":                      "Authentication: anonymous Quick review identity available locally.",
		"setup_auth_unavailable":                    "Authentication: credentials are missing, expired, or rejected.",
		"setup_auth_unknown":                        "Authentication: could not verify credentials with the configured service.",
		"setup_upload_ready":                        "Upload approval: saved approval covers this checkout, agent, and service.",
		"setup_upload_missing":                      "Upload approval: no saved approval for this checkout, agent, and service.",
		"setup_upload_unknown":                      "Upload approval: could not verify saved approval.",
	}
	for _, check := range checks {
		fmt.Fprintln(c.OutOrStdout(), messages[check])
	}
}
