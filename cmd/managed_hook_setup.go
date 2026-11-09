package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
)

type managedHookSetup struct {
	State          string   `json:"state"`
	ConfigPath     string   `json:"config_path"`
	Activation     string   `json:"activation"`
	Decision       string   `json:"decision,omitempty"`
	Error          string   `json:"error,omitempty"`
	Action         string   `json:"action"`
	InstallCommand []string `json:"install_command,omitempty"`
	DeclineCommand []string `json:"decline_command,omitempty"`
}

func hookSetupDecisionPath(s managed.Store, t managed.Task, consent *managedConsentEvidence, path string) string {
	return filepath.Join(s.Root, "hook-setup-decisions", watch.Key(t.Agent+"\x00"+path+"\x00"+managedSetupScope(consent)))
}

func managedSetupScope(consent *managedConsentEvidence) string {
	scope, _ := json.Marshal(struct {
		Account, Repository, Service, Profile, Agent string
		AllRepositories                              bool
	}{consent.Account, consent.Repository, consent.Service, consent.Profile, consent.Agent, consent.AllRepositories})
	return string(scope)
}

func inspectManagedHookSetup(s managed.Store, t managed.Task, consent *managedConsentEvidence) *managedHookSetup {
	home, err := os.UserHomeDir()
	p := &managedHookSetup{State: "unreadable", Activation: "unverified", Action: "Report hook configuration errors without changing trust or upload consent."}
	if err != nil {
		p.Error = err.Error()
		return p
	}
	p.ConfigPath = watch.HookFile(home, t.Agent)
	installed, err := watch.Installed(p.ConfigPath, "--managed-task-adapter")
	if err != nil {
		p.Error = err.Error()
		return p
	}
	if installed {
		p.State = "configured"
		p.Action = "Hook definitions are installed; this does not prove native trust. If the current harness reports untrusted hooks, prompt the user to review and trust them through its native /hooks controls. Remind once per new session while untrusted, even after an earlier session declined trust. Never edit trust records or retry a trust refusal in the same session. Otherwise report activation as unverified until a real callback is observed."
		if t.HookActivated {
			p.Activation = "observed_for_task"
		}
		return p
	}
	p.State = "missing"
	p.Decision = "pending"
	var decision string
	if err := watch.Read(hookSetupDecisionPath(s, t, consent, p.ConfigPath), &decision); err == nil && decision == "declined" {
		p.Decision = "declined"
		p.Action = "The user declined this scoped hook installation. Preserve that choice across tasks and upgrades; do not ask again unless the user revisits it. Continue permitted per-call reviews."
		return p
	} else if err != nil && !os.IsNotExist(err) {
		p.Error = err.Error()
	}
	binary, err := os.Executable()
	if err != nil {
		p.Error = err.Error()
		return p
	}
	base := []string{binary, "--api-url", t.API, "--profile", t.Profile, "task", "hook-setup", t.Handle}
	p.InstallCommand = append(append([]string{}, base...), "--allow")
	p.DeclineCommand = append(append([]string{}, base...), "--decline")
	p.Action = "Doomer hooks are missing. Offer installation during onboarding or upgrade unless already authorized or explicitly declined. After acceptance execute install_command and verify configured; after an explicit decline execute decline_command. Do not interpret silence as a decline. Preserve unrelated hooks and native trust controls. For Claude, accepted setup also installs its four scoped review-command permission rules. Upload consent remains separate."
	return p
}

func recordHookSetupDecline(s managed.Store, t managed.Task, consent *managedConsentEvidence) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return watch.Write(hookSetupDecisionPath(s, t, consent, watch.HookFile(home, t.Agent)), "declined")
}

func clearHookSetupDecline(s managed.Store, t managed.Task, consent *managedConsentEvidence) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	err = os.Remove(hookSetupDecisionPath(s, t, consent, watch.HookFile(home, t.Agent)))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear hook setup decision: %w", err)
	}
	return nil
}
