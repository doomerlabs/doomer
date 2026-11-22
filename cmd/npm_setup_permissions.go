package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

type npmClaudePermission struct {
	Decision       string   `json:"decision"`
	File           string   `json:"file"`
	OwnedRules     []string `json:"ownedRules"`
	OwnedAutoRules []string `json:"ownedAutoRules"`
}

func npmClaudeRules(paths []string) ([]string, []string, error) {
	rules := []string{}
	for _, path := range paths {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, " \t\n\r*()?[]{}'\"`$\\") {
			return nil, nil, fmt.Errorf("Claude reviewer permission requires an absolute CLI path without rule metacharacters")
		}
		for _, command := range []string{"hypotheses", "findings respond", "findings ls", "results", "notice-ack"} {
			rules = append(rules, fmt.Sprintf("Bash(%s watch %s --control *)", path, command))
		}
	}
	var formatted bytes.Buffer
	encoder := json.NewEncoder(&formatted)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(paths); err != nil {
		return nil, nil, err
	}
	auto := fmt.Sprintf("Doomer review collaboration: permit only %s invoking watch hypotheses, watch findings respond, watch findings ls, watch results, or watch notice-ack with an active --control key for the user's approved Doomer service. Assessments, fixed/dismissed/deferred decisions, agreement flags, and repository-specific explanations are authorized review inputs, not changes to the reviewer or its rules. Acknowledge advisories only after displaying them. This does not permit changing reviewer configuration, installing software, arbitrary shell commands, or overriding explicit denials.", strings.TrimSuffix(formatted.String(), "\n"))
	return rules, []string{auto}, nil
}

func (s npmSettings) claudePermission(c *cobra.Command, o npmSetupOptions, p npmSetupPrompt, changes *doctorRepairChanges) error {
	state, err := readNPMSetupState(o.home)
	if err != nil {
		return err
	}
	var previous npmClaudePermission
	if err := state.get("claudeReviewPermission", &previous); err != nil {
		return err
	}
	decision := o.claudeDecision
	if decision == "" {
		decision = previous.Decision
	}
	if decision == "" && o.interactive {
		resolved, err := filepath.EvalSymlinks(o.executable)
		if err != nil {
			return err
		}
		paths := appendUnique([]string{o.executable}, resolved)
		rules, auto, ruleErr := npmClaudeRules(paths)
		details := strings.Join(append(rules, auto...), "\n")
		if ruleErr != nil {
			details = ruleErr.Error()
		}
		fmt.Fprintln(p.output, "Claude can read results, submit assessments and finding decisions, and acknowledge advisories you’ve seen. This applies only to active Doomer reviews.")
		decision, err = npmAskPermission(c, p, "Allow Claude Code to handle Doomer reviews?", "Allow review commands", details)
		if err != nil {
			return err
		}
	}
	if decision == "" || decision == "decline" && o.claudeDecision == "" && previous.Decision == "decline" {
		return nil
	}
	file := s.files["claude"][0]
	unlock, err := npmSetupLock(file)
	if err != nil {
		return err
	}
	defer unlock()
	unlockState, err := npmSetupLock(filepath.Join(o.home, "state.json"))
	if err != nil {
		return err
	}
	defer unlockState()
	state, err = readNPMSetupState(o.home)
	if err != nil {
		return err
	}
	var current npmClaudePermission
	if err := state.get("claudeReviewPermission", &current); err != nil {
		return err
	}
	if current.Decision != previous.Decision || current.File != previous.File ||
		!slices.Equal(current.OwnedRules, previous.OwnedRules) || !slices.Equal(current.OwnedAutoRules, previous.OwnedAutoRules) {
		return fmt.Errorf("Claude reviewer permission changed during setup; retry to use the latest choice")
	}
	read := npmReadSettings
	if o.mode == "doctor" {
		if current.Decision == "decline" || current.File != "" && current.File != file {
			return fmt.Errorf("this Claude profile has an existing reviewer permission choice")
		}
		if _, err := os.Lstat(filepath.Join(o.home, "uninstalling.json")); !os.IsNotExist(err) {
			return fmt.Errorf("personal Doomer installer is unavailable")
		}
		var executable string
		var files []string
		if state.get("executable", &executable) != nil || executable != o.executable ||
			state.get("hookFiles", &files) != nil || !slices.Contains(files, file) {
			return fmt.Errorf("the installer configuration changed during diagnosis")
		}
		read = readDoctorAutoHookSettings
	}
	config, before, err := read(file)
	if err != nil {
		return err
	}
	if o.mode == "doctor" {
		for _, key := range []string{"permissions", "autoMode"} {
			if block, ok := config[key].(npmJSONObject); ok {
				for _, restriction := range []string{"deny", "ask", "soft_deny", "hard_deny"} {
					if rules, present := block[restriction]; present {
						if entries, valid := rules.([]any); !valid || len(entries) > 0 {
							return fmt.Errorf("Claude %s already has %s rules; keep that policy in effect", key, restriction)
						}
					}
				}
			}
		}
	}
	var rules, auto []string
	if decision == "allow" {
		resolved, err := filepath.EvalSymlinks(o.executable)
		if err != nil {
			return err
		}
		if o.mode == "doctor" {
			current, currentErr := os.Stat(mustExecutable())
			installed, installedErr := os.Stat(resolved)
			if currentErr != nil || installedErr != nil || !os.SameFile(current, installed) {
				return fmt.Errorf("the installed executable changed during diagnosis")
			}
		}
		rules, auto, err = npmClaudeRules(appendUnique([]string{o.executable}, resolved))
		if err != nil {
			return err
		}
	}
	if file != previous.File {
		previous.OwnedRules, previous.OwnedAutoRules = nil, nil
	}
	priorRules, priorAuto := previous.OwnedRules, previous.OwnedAutoRules
	if o.mode == "doctor" {
		// Doctor only adds missing rules. Keep older installer-owned entries
		// intact and tracked for later installer maintenance.
		priorRules, priorAuto = nil, nil
	}
	owned, changed, err := npmMergeClaudeAllow(config, "permissions", nil, priorRules, rules)
	if err != nil {
		return err
	}
	ownedAuto, autoChanged, err := npmMergeClaudeAllow(config, "autoMode", []string{"$defaults"}, priorAuto, auto)
	if err != nil {
		return err
	}
	if o.mode == "doctor" {
		owned = appendUnique(slices.Clone(previous.OwnedRules), owned...)
		ownedAuto = appendUnique(slices.Clone(previous.OwnedAutoRules), ownedAuto...)
	}
	next := npmClaudePermission{decision, file, owned, ownedAuto}
	if changed || autoChanged {
		if o.mode == "doctor" && before != nil {
			backup := fmt.Sprintf("%s.before-doomer-node-%d", file, time.Now().UnixNano())
			if err := writeNPMFile(backup, before, 0600); err != nil {
				return err
			}
		}
		ledger := next
		ledger.OwnedRules = appendUnique(slices.Clone(previous.OwnedRules), owned...)
		ledger.OwnedAutoRules = appendUnique(slices.Clone(previous.OwnedAutoRules), ownedAuto...)
		state.set("claudeReviewPermission", ledger)
		if err := writeNPMSetupState(o.home, state); err != nil {
			return err
		}
		if err := watching.Write(file, config); err != nil {
			return err
		}
		if changes != nil {
			changes.settings(file, func(config npmJSONObject) npmJSONObject {
				return doctorPermissionDiffSettings(config, rules, auto)
			})(before, config)
		}
	}
	state.set("claudeReviewPermission", next)
	return writeNPMSetupState(o.home, state)
}

func npmMergeClaudeAllow(config npmJSONObject, key string, defaults, previous, desired []string) ([]string, bool, error) {
	block := npmJSONObject{}
	var before []string
	if value, ok := config[key]; ok {
		block, ok = value.(npmJSONObject)
		if !ok {
			return nil, false, fmt.Errorf("invalid Claude %s settings", key)
		}
		if value, ok := block["allow"]; ok {
			values, valid := value.([]any)
			if !valid {
				return nil, false, fmt.Errorf("invalid Claude %s allow rules", key)
			}
			before = make([]string, 0, len(values))
			for _, value := range values {
				rule, ok := value.(string)
				if !ok {
					return nil, false, fmt.Errorf("invalid Claude %s allow rule", key)
				}
				before = append(before, rule)
			}
		}
	}
	owned := []string{}
	if len(previous) == 0 && len(desired) == 0 {
		return owned, false, nil
	}
	base := before
	if base == nil {
		base = defaults
	}
	next := []string{}
	for _, rule := range base {
		if !slices.Contains(previous, rule) || slices.Contains(desired, rule) {
			next = append(next, rule)
		}
		if slices.Contains(previous, rule) && slices.Contains(desired, rule) {
			owned = appendUnique(owned, rule)
		}
	}
	for _, rule := range desired {
		if !slices.Contains(next, rule) {
			owned = append(owned, rule)
			next = append(next, rule)
		}
	}
	changed := !slices.Equal(before, next)
	if changed {
		config[key] = block
		if len(next) == 0 {
			delete(block, "allow")
		} else {
			block["allow"] = next
		}
	}
	return owned, changed, nil
}

type npmCodexPolicyReport struct {
	Policies   []codexReviewPolicy `json:"policies"`
	Activation string              `json:"activation"`
}

func npmCodexPolicyCall(ctx context.Context, o npmSetupOptions, api, file, decision, proposal string) (npmCodexPolicyReport, error) {
	args := []string{"--api-url", api, "watch", "codex-policy", "--manager-home", o.home}
	if decision != "" {
		args = append(args, "--decision", decision, "--proposal", proposal)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call := exec.CommandContext(ctx, o.executable, args...)
	call.Env = append(os.Environ(), "CODEX_HOME="+filepath.Dir(file))
	var errOut bytes.Buffer
	call.Stderr = &errOut
	out, err := call.Output()
	if err != nil {
		return npmCodexPolicyReport{}, fmt.Errorf("inspect or apply Codex reviewer policy for %s: %s", file, strings.TrimSpace(errOut.String()))
	}
	var report npmCodexPolicyReport
	err = json.Unmarshal(out, &report)
	return report, err
}

func npmCodexProposal(policy codexReviewPolicy) string {
	i := slices.Index(policy.InstallCommand, "--proposal")
	if i < 0 || i+1 >= len(policy.InstallCommand) {
		return ""
	}
	return policy.InstallCommand[i+1]
}

func npmCodexPermissions(c *cobra.Command, o npmSetupOptions, files []string, stateAPI string, state npmSetupState, p npmSetupPrompt) error {
	consent, err := state.consent()
	if err != nil {
		return err
	}
	if _, err := agentHookConsent(consent, stateAPI, "codex", ""); err != nil {
		if o.codexDecision != "" {
			return fmt.Errorf("upload consent is required before deciding a reviewer policy")
		}
		return nil
	}
	type target struct {
		file   string
		report npmCodexPolicyReport
	}
	targets := []target{}
	matched := false
	for _, file := range files {
		report, err := npmCodexPolicyCall(c.Context(), o, stateAPI, file, "", "")
		if err != nil {
			return err
		}
		targets = append(targets, target{file, report})
		for _, policy := range report.Policies {
			matched = matched || o.proposal != "" && npmCodexProposal(policy) == o.proposal
		}
	}
	if o.codexDecision != "" && !matched {
		return fmt.Errorf("a matching reviewed proposal is required; display the current proposal and pass its --reviewer-policy-proposal digest")
	}
	type ownedPolicy struct {
		ConfigPath  string `json:"configPath"`
		ExtraPolicy string `json:"extraPolicy"`
	}
	installed := []ownedPolicy{}
	for _, t := range targets {
		for _, policy := range t.report.Policies {
			digest := npmCodexProposal(policy)
			if digest == "" {
				continue
			}
			decision := o.codexDecision
			if decision != "" && digest != o.proposal {
				continue
			}
			if decision == "" && o.interactive {
				fmt.Fprintf(p.output, "Codex can submit assessments and finding decisions within your saved Doomer upload approval. Settings: %s\n", policy.ConfigPath)
				decision, err = npmAskPermission(c, p, "Allow Codex to handle Doomer reviews?", "Allow Doomer reviews", policy.ExtraPolicy+"\n"+strings.Join(policy.Changes, "\n"))
				if err != nil {
					return err
				}
			}
			if decision == "" {
				continue
			}
			if _, err := npmCodexPolicyCall(c.Context(), o, stateAPI, t.file, decision, digest); err != nil {
				return err
			}
			if decision == "allow" {
				installed = append(installed, ownedPolicy{policy.ConfigPath, policy.ExtraPolicy})
			}
		}
	}
	if len(installed) != 0 {
		unlock, err := npmSetupLock(filepath.Join(o.home, "state.json"))
		if err != nil {
			return err
		}
		defer unlock()
		current, err := readNPMSetupState(o.home)
		if err != nil {
			return err
		}
		var owned []ownedPolicy
		if err := current.get("codexPolicies", &owned); err != nil {
			return err
		}
		for _, policy := range installed {
			if !slices.Contains(owned, policy) {
				owned = append(owned, policy)
			}
		}
		current.set("codexPolicies", owned)
		if err := writeNPMSetupState(o.home, current); err != nil {
			return err
		}
	}
	return nil
}
