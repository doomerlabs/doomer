package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/spf13/cobra"
)

// Remove only full, recognized blocks tied to credential stores recorded by this
// manager. No account request is needed, and other installation scopes survive.
func stripNodeCodexPolicies(policy string, state agentHookState, preserve []string) string {
	const heading = "Doomer repository review authorization\n"
	for offset := 0; offset < len(policy); {
		at := strings.Index(policy[offset:], heading)
		if at < 0 {
			break
		}
		at += offset
		lines := strings.SplitN(policy[at:], "\n", 4)
		if len(lines) < 4 {
			break
		}
		var scope struct {
			CredentialDirectory string `json:"credential_directory"`
			Account             string `json:"account"`
			Repository          string `json:"repository"`
			Service             string `json:"service"`
			Profile             string `json:"profile"`
			Agent               string `json:"agent"`
			AllRepositories     bool   `json:"all_repositories"`
		}
		if json.Unmarshal([]byte(lines[2]), &scope) == nil && scope.CredentialDirectory != "" &&
			state.CredentialDirs[scope.Service] == scope.CredentialDirectory && (scope.Agent == "codex" || scope.Agent == "") && !containsString(preserve, scope.Service) {
			consent := managedConsentEvidence{CredentialDirectory: scope.CredentialDirectory, Account: scope.Account, Repository: scope.Repository, Service: scope.Service, Profile: scope.Profile, Agent: scope.Agent, AllRepositories: scope.AllRepositories}
			removed := false
			for _, explicit := range []bool{true, false} {
				block := codexReviewPolicyText(consent, explicit).ExtraPolicy
				if strings.HasPrefix(policy[at:], block) {
					policy = policy[:at] + policy[at+len(block):]
					removed = true
					break
				}
			}
			if removed {
				offset = at
				continue
			}
		}
		offset = at + len(heading)
	}
	return policy
}

func containsString(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func removeNodeCodexPolicies(c *cobra.Command, state agentHookState, preserve []string) error {
	path, err := codexConfigPath()
	if err != nil {
		return err
	}
	lock, err := publock.Acquire(filepath.Dir(path), "doomer-codex-policy")
	if err != nil {
		return err
	}
	defer lock.Close()
	data, err := readCodexConfig(path)
	if err != nil {
		return err
	}
	policy, err := codexExtraPolicy(data)
	if err != nil {
		return err
	}
	stripped := stripNodeCodexPolicies(policy, state, preserve)
	for _, saved := range state.CodexPolicies {
		if saved.ConfigPath == path && saved.ExtraPolicy != "" {
			// Only recorded exact text belongs to this installation. Modified copies
			// and scopes explicitly shared with another installation remain untouched.
			owned := agentHookState{CredentialDirs: state.CredentialDirs}
			recognized := stripNodeCodexPolicies(saved.ExtraPolicy, owned, preserve)
			if recognized == "" || (len(preserve) == 0 && strings.HasPrefix(saved.ExtraPolicy, "Doomer repository review authorization\n")) {
				stripped = strings.ReplaceAll(stripped, saved.ExtraPolicy, "")
			}
		}
	}
	changed := stripped != policy
	if changed {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Codex config must be a regular file")
		}
		out, err := patchCodexExtraPolicy(data, stripped)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(filepath.Dir(path), ".doomer-uninstall-*")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if err = f.Chmod(info.Mode().Perm()); err == nil {
			_, err = f.Write(out)
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		current, err := readCodexConfig(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, data) {
			return fmt.Errorf("Codex config changed during removal; retry")
		}
		if err := os.Rename(f.Name(), path); err != nil {
			return err
		}
	}
	return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"removed": true, "changed": changed, "config_path": path, "preserved_policy": strings.Contains(stripped, "Doomer repository review authorization\n")})
}
