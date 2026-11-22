package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func readDoctorCodexConfig(path string) ([]byte, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return nil, fmt.Errorf("Doctor requires a regular Codex config no larger than 4 MiB")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return readCodexConfig(path)
}

// Restore only the exact policy that this installer previously recorded and
// that still matches the credential and upload grant for the current checkout.
func restoreDoctorCodexPermission(c *cobra.Command, root, manager string, target doctorAutoTarget, changes *doctorRepairChanges) (string, error) {
	before := len(*changes)
	err := restoreDoctorCodexPolicy(c, root, manager, target, changes)
	if err != nil {
		fmt.Fprintf(c.ErrOrStderr(), "Could not restore the Codex reviewer policy: %v\n", err)
		return "setup_agent_permissions_repair_failed", err
	}
	if len(*changes) == before {
		return "setup_agent_permissions_recorded", nil
	}
	return "setup_agent_permissions_repaired", nil
}

func restoreDoctorCodexPolicy(c *cobra.Command, root, manager string, target doctorAutoTarget, changes *doctorRepairChanges) error {
	unlock, err := npmSetupLock(filepath.Join(manager, "state.json"))
	if err != nil {
		return err
	}
	defer unlock()
	current, err := loadDoctorAutoTarget(root, manager, "codex")
	if err != nil {
		return err
	}
	if current != target {
		return fmt.Errorf("Doomer configuration changed during diagnosis")
	}
	state, err := readNPMSetupState(manager)
	if err != nil {
		return err
	}
	consent, err := state.consent()
	if err != nil {
		return err
	}
	env, err := directoryWatchEnvironment(root)
	if err != nil {
		return err
	}
	api, err := agentHookAPI("", env.get("DOOMER_API_URL"), consent.API)
	if err != nil {
		return err
	}
	if api != strings.TrimRight(consent.API, "/") || env.get("DOOMER_TOKEN") != "" {
		return fmt.Errorf("restore Codex permissions using the installer’s configured service and credentials")
	}
	grant, err := agentHookConsent(consent, api, "codex", root)
	if err != nil {
		return err
	}
	token, err := watchToken(c, api, consent, env)
	if err != nil {
		return err
	}
	dir, profile, err := watchCredentialProfile(c, api, consent, env)
	if err != nil {
		return err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return err
	}
	account := "anonymous:" + watching.Key(token)
	if !adversarylabs.IsAnonymousToken(token) {
		ctx, cancel := context.WithTimeout(c.Context(), 4*time.Second)
		defer cancel()
		who, err := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, api).Whoami(ctx, token)
		if err != nil || who.ID == "" {
			return fmt.Errorf("could not verify the installer’s account")
		}
		account = who.ID
	}
	evidence := managedConsentEvidence{Account: account, Service: api, Profile: profile, CredentialDirectory: dir, Agent: "codex", AllRepositories: grant.AllRepositories}
	if !grant.AllRepositories {
		evidence.Repository, err = watching.RepositoryIdentity(c.Context(), root)
		if err != nil {
			return err
		}
	}
	path := filepath.Join(filepath.Dir(target.file), "config.toml")
	if _, err := readDoctorCodexConfig(path); err != nil {
		return err
	}
	proposal := proposedCodexReviewPolicy(evidence)
	proposal.ConfigPath = path
	if !slices.Contains(consent.CodexPolicies, nodeCodexPolicy{ConfigPath: path, ExtraPolicy: proposal.ExtraPolicy}) {
		return fmt.Errorf("no previously approved Codex policy matches the current installer scope")
	}
	store := managed.Store{Root: filepath.Join(dir, "managed")}
	changed := false
	err = updateCodexConfig(path, func(data []byte) ([]byte, error) {
		if len(data) > 4<<20 {
			return nil, fmt.Errorf("Codex config is too large")
		}
		policy, err := codexExtraPolicy(data)
		if err != nil {
			return nil, err
		}
		proposal.configData = []byte(policy)
		declined, err := codexReviewPolicyDeclined(store, proposal)
		if err != nil {
			return nil, err
		}
		legacy := *proposal
		legacyEvidence := evidence
		legacyEvidence.CredentialDirectory = ""
		legacy.ExtraPolicy = proposedCodexReviewPolicy(legacyEvidence).ExtraPolicy
		legacyDeclined, err := codexReviewPolicyDeclined(store, &legacy)
		if err != nil {
			return nil, err
		}
		if declined || legacyDeclined {
			return nil, fmt.Errorf("the user declined this Codex reviewer policy")
		}
		if strings.Contains(policy, proposal.ExtraPolicy) {
			return data, nil
		}
		if strings.Contains(policy, "Doomer repository review authorization\n") {
			return nil, fmt.Errorf("the Codex settings contain other Doomer policy text that needs manual review")
		}
		out, err := patchCodexExtraPolicy(data, mergeCodexExtraPolicy(policy, evidence))
		changed = !bytes.Equal(data, out)
		return out, err
	})
	if err == nil && changed {
		changes.codexPolicy(path, proposal.ExtraPolicy)
	}
	return err
}
