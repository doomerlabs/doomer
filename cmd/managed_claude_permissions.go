package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
)

// These rules allow only the task review interface, not install, reset, arbitrary
// shell execution, or configuration changes. Every operation still checks the
// task's live account, saved upload consent, and pause state inside Doomer.
var claudeReviewRules = []string{
	"Bash(doomer task submit *)",
	"Bash(doomer task status *)",
	"Bash(doomer task respond *)",
	"Bash(doomer task finish *)",
}

type claudePermissions struct {
	ConfigPath string   `json:"config_path"`
	State      string   `json:"state"`
	Rules      []string `json:"rules"`
	Changes    []string `json:"changes"`
	Activation string   `json:"activation"`
	Error      string   `json:"error,omitempty"`
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
		"Normal approved Claude onboarding installs these four review-command rules with the hooks, preserving all other settings.",
		"The rules apply to these command forms across repositories. Doomer still requires live, matching upload consent for each task; these rules grant no Doomer upload permission.",
		"Use the canonical doomer task commands. Different executable spellings, managed policy, sandbox restrictions, or classifier settings may still require approval. Do not disable those controls.",
	}}
	var err error
	p.ConfigPath, err = claudePermissionPath()
	if err != nil {
		p.State, p.Error = "unreadable", err.Error()
		return p
	}
	_, _, allow, err := readClaudePermissionConfig(p.ConfigPath)
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
	return p
}

func installClaudePermissions(s managed.Store) error {
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
	if before != nil {
		if err = os.WriteFile(path+".before-doomer-permissions-"+time.Now().UTC().Format("20060102T150405.000000000"), before, 0600); err != nil {
			return err
		}
	}
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
	if err = resetWriteFile(path, before, written, mode); err != nil {
		return err
	}
	if err = watch.Write(receiptPath, receipt); err != nil {
		rollbackErr := resetWriteFile(path, written, before, mode)
		return fmt.Errorf("save Claude permission ownership: %w (rollback: %v)", err, rollbackErr)
	}
	return nil
}
