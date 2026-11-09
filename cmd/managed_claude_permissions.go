package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
)

// These rules allow readiness and task review interfaces, not install, reset, arbitrary
// shell execution, or configuration changes. Every operation still checks the
// task's live account, saved upload consent, and pause state inside Doomer.
var claudeReviewRules = []string{
	"Bash(doomer preflight *)",
	"Bash(doomer prepare *)",
	"Bash(doomer task submit *)",
	"Bash(doomer task status *)",
	"Bash(doomer task respond *)",
	"Bash(doomer task finish *)",
}

type claudePermissions struct {
	ConfigPath     string   `json:"config_path"`
	State          string   `json:"state"`
	Rules          []string `json:"rules"`
	Changes        []string `json:"changes"`
	Activation     string   `json:"activation"`
	Error          string   `json:"error,omitempty"`
	DeniedRules    []string `json:"denied_rules,omitempty"`
	Decision       string   `json:"decision,omitempty"`
	InstallCommand []string `json:"install_command,omitempty"`
	DeclineCommand []string `json:"decline_command,omitempty"`
	Action         string   `json:"action,omitempty"`
}

type claudePermissionReceipt struct {
	ConfigPath string   `json:"config_path"`
	Added      []string `json:"added"`
}

func claudePermissionPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func readClaudePermissionConfig(path string) ([]byte, map[string]any, []any, error) {
	for _, p := range []string{filepath.Dir(path), path} {
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, nil, fmt.Errorf("refusing symlinked Claude settings: %s", p)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, nil, err
	}
	obj := map[string]any{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
			return nil, nil, nil, fmt.Errorf("invalid Claude settings JSON")
		}
	}
	permissions, ok := obj["permissions"].(map[string]any)
	if !ok {
		if obj["permissions"] != nil {
			return nil, nil, nil, fmt.Errorf("invalid Claude permissions object")
		}
		permissions = map[string]any{}
		obj["permissions"] = permissions
	}
	allow, ok := permissions["allow"].([]any)
	if !ok && permissions["allow"] != nil {
		return nil, nil, nil, fmt.Errorf("invalid Claude allow rules")
	}
	for _, rule := range allow {
		if _, ok := rule.(string); !ok {
			return nil, nil, nil, fmt.Errorf("invalid Claude allow rule")
		}
	}
	return data, obj, allow, nil
}

func inspectClaudePermissions() *claudePermissions {
	p := &claudePermissions{Rules: slices.Clone(claudeReviewRules), State: "missing", Activation: "unverified", Changes: []string{
		"Normal approved Claude onboarding installs these six readiness and review-command rules with the hooks, preserving all other settings.",
		"The rules apply to these command forms across repositories. Doomer still requires live, matching upload consent for each task; these rules grant no Doomer upload permission.",
		"Use the canonical doomer task commands. Different executable spellings, managed policy, sandbox restrictions, or classifier settings may still require approval. Do not disable those controls.",
	}}
	var err error
	p.ConfigPath, err = claudePermissionPath()
	if err != nil {
		p.State, p.Error = "unreadable", err.Error()
		return p
	}
	_, obj, allow, err := readClaudePermissionConfig(p.ConfigPath)
	if err != nil {
		p.State, p.Error = "unreadable", err.Error()
		return p
	}
	p.State = "configured"
	for _, rule := range p.Rules {
		if !slices.Contains(allow, any(rule)) {
			p.State = "missing"
		}
	}
	permissions, _ := obj["permissions"].(map[string]any)
	deny, _ := permissions["deny"].([]any)
	for _, raw := range deny {
		rule, ok := raw.(string)
		if !ok {
			continue
		}
		for _, command := range []string{"doomer preflight --agent claude --repo .", "doomer prepare --agent claude --repo .", "doomer task submit example-task", "doomer task status example-task", "doomer task respond example-task", "doomer task finish example-task"} {
			pattern := strings.TrimSuffix(strings.TrimPrefix(rule, "Bash("), ")")
			if rule == "Bash" || (strings.HasPrefix(rule, "Bash(") && regexp.MustCompile("^"+strings.ReplaceAll(regexp.QuoteMeta(strings.ReplaceAll(pattern, ":*", " *")), `\*`, ".*")+"$").MatchString(command)) {
				p.DeniedRules = append(p.DeniedRules, rule)
				break
			}
		}
	}
	if len(p.DeniedRules) > 0 {
		p.State = "denied"
		p.Action = "Explicit Claude deny rules cover Doomer review commands. Preserve them and do not offer an allow rule to override the denial. Report the blocked operations; changing deny rules requires the user's explicit request."
	}
	return p
}

func claudePermissionDecisionPath(s managed.Store, t managed.Task, evidence *managedConsentEvidence) (string, error) {
	path, err := claudePermissionPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(s.Root, "claude-permission-decisions", watch.Key(path+"\x00"+t.Agent+"\x00"+managedSetupScope(evidence))), nil
}

func proposedClaudePermissions(s managed.Store, t managed.Task, evidence *managedConsentEvidence) *claudePermissions {
	p := inspectClaudePermissions()
	if p.State != "missing" {
		return p
	}
	var decision string
	decisionPath, err := claudePermissionDecisionPath(s, t, evidence)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	if err := watch.Read(decisionPath, &decision); err == nil && decision == "declined" {
		p.Decision = "declined"
		p.Action = "This scoped Claude permission setup was declined. Do not ask again unless the user revisits that decision. Preserve existing upload consent and host restrictions."
		return p
	}
	p.Decision = "pending"
	binary, err := os.Executable()
	if err != nil {
		p.Error = err.Error()
		return p
	}
	base := []string{binary, "--api-url", t.API, "--profile", t.Profile, "task", "claude-permissions", t.Handle}
	p.InstallCommand = append(append([]string{}, base...), "--allow")
	p.DeclineCommand = append(append([]string{}, base...), "--decline")
	p.Action = "Claude review-command permissions are missing. Present rules and changes and ask to install unless already authorized. Execute install_command only after acceptance; record an explicit decline with decline_command. An unanswered offer remains pending for later chats; silence is not a decline. These settings do not grant repository upload consent and cannot override deny rules."
	return p
}

func installClaudePermissions(s managed.Store) error {
	return installClaudePermissionsWithWriters(s, resetWriteFile, watch.Write)
}

func installClaudePermissionsWithWriters(s managed.Store,
	writeSettings func(string, []byte, []byte, os.FileMode) error,
	writeReceipt func(string, any) error,
) (resultErr error) {
	if p := inspectClaudePermissions(); p.State == "denied" {
		return fmt.Errorf("Claude deny rules block Doomer review commands; preserve the user's denial")
	}
	path, err := claudePermissionPath()
	if err != nil {
		return err
	}
	unlock, err := watchLock(s.Root, "claude-permissions-"+watch.Key(path))
	if err != nil {
		return err
	}
	defer unlock()
	before, obj, allow, err := readClaudePermissionConfig(path)
	if err != nil {
		return err
	}
	receiptPath := filepath.Join(s.Root, "claude-permissions", watch.Key(path)+".json")
	receipt := claudePermissionReceipt{ConfigPath: path}
	if err = watch.Read(receiptPath, &receipt); err != nil && !os.IsNotExist(err) {
		return err
	}
	if receipt.ConfigPath != path {
		return fmt.Errorf("Claude permission receipt path mismatch")
	}
	for _, rule := range claudeReviewRules {
		if !slices.Contains(allow, any(rule)) {
			allow = append(allow, rule)
			if !slices.Contains(receipt.Added, rule) {
				receipt.Added = append(receipt.Added, rule)
			}
		}
	}
	if inspectClaudePermissions().State == "configured" {
		return nil
	}
	obj["permissions"].(map[string]any)["allow"] = allow
	after, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	var backupPath string
	if before != nil {
		backupPath = path + ".before-doomer-permissions-" + time.Now().UTC().Format("20060102T150405.000000000")
		if err = os.WriteFile(backupPath, before, 0600); err != nil {
			return err
		}
	}
	defer func() {
		if resultErr == nil || backupPath == "" {
			return
		}
		// Remove this attempt's backup only when the settings are demonstrably
		// unchanged or restored. Keep recovery evidence after a failed rollback
		// or a concurrent edit; never delete an earlier attempt's backup.
		current, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(current, before) {
			if removeErr := os.Remove(backupPath); removeErr != nil && !os.IsNotExist(removeErr) {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove unused Claude settings backup: %w", removeErr))
			}
		}
	}()
	if before == nil {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		before = []byte{}
	}
	mode := os.FileMode(0600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	written := append(after, '\n')
	if err = writeSettings(path, before, written, mode); err != nil {
		return err
	}
	if err = writeReceipt(receiptPath, receipt); err != nil {
		rollbackErr := writeSettings(path, written, before, mode)
		return fmt.Errorf("save Claude permission ownership: %w (rollback: %v)", err, rollbackErr)
	}
	return nil
}
