package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doomerlabs/doomer/internal/managed"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

// Node owns upload consent. This adapter reuses Go's scoped proposal, decline,
// template repair and atomic merge; it never grants uploads or trusts hooks.
func newNodeCodexPolicyCommand() *cobra.Command {
	var home, decision, expected string
	c := &cobra.Command{Use: "codex-policy", Short: "Inspect or decide the Node installer's scoped Codex reviewer policy", Args: cobra.NoArgs}
	c.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	c.Flags().StringVar(&home, "manager-home", "", "Node installer state directory")
	c.Flags().StringVar(&decision, "decision", "", "explicit allow or decline decision")
	c.Flags().StringVar(&expected, "proposal", "", "exact proposal digest returned by inspection")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if !filepath.IsAbs(home) {
			return fmt.Errorf("absolute manager home required")
		}
		if decision != "" && decision != "allow" && decision != "decline" {
			return fmt.Errorf("invalid policy decision")
		}
		var state agentHookState
		if err := watching.Read(filepath.Join(home, "state.json"), &state); err != nil {
			return err
		}
		directory, err := os.Getwd()
		if err != nil {
			return err
		}
		env, err := directoryWatchEnvironment(directory)
		if err != nil {
			return err
		}
		explicit := ""
		if c.Flags().Changed("api-url") || c.InheritedFlags().Changed("api-url") {
			explicit, _ = c.Flags().GetString("api-url")
		}
		api, err := agentHookAPI(explicit, env.get("DOOMER_API_URL"), state.API)
		if err != nil {
			return err
		}
		cfg, err := agentHookConsent(state, api, "codex", "")
		if err != nil {
			return err
		}
		token, err := watchToken(c, api, state, env)
		if err != nil {
			return err
		}
		dir, profile, err := watchCredentialProfile(c, api, state, env)
		if err != nil {
			return err
		}
		dir, err = filepath.Abs(dir)
		if err != nil {
			return err
		}
		account := "anonymous:" + watching.Key(token)
		if !adversarylabs.IsAnonymousToken(token) {
			who, err := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, api).Whoami(c.Context(), token)
			if err != nil {
				return err
			}
			if who.ID == "" {
				return fmt.Errorf("account identity missing")
			}
			account = who.ID
		}
		store := managed.Store{Root: filepath.Join(dir, "managed")}
		scopes := []string{""}
		if !cfg.AllRepositories {
			scopes = cfg.Directories
		}
		proposals := []*codexReviewPolicy{}
		matched := false
		for _, repo := range scopes {
			identity := ""
			if repo != "" {
				identity, err = watching.RepositoryIdentity(c.Context(), repo)
				if err != nil {
					return err
				}
			}
			consent := managedConsentEvidence{Account: account, Service: api, Profile: profile, CredentialDirectory: dir, Agent: "codex", AllRepositories: cfg.AllRepositories, Repository: identity}
			proposal, _, err := inspectCodexReviewPolicy(consent)
			if err != nil {
				return err
			}
			digest := watching.Key(codexReviewPolicyDecisionPath(store, proposal))
			declined, err := codexReviewPolicyDeclined(store, proposal)
			if err != nil {
				return err
			}

			if !declined {
				legacyConsent := consent
				legacyConsent.CredentialDirectory = ""
				legacy, _, e := inspectCodexReviewPolicy(legacyConsent)
				if e != nil {
					return e
				}
				declined, err = codexReviewPolicyDeclined(store, legacy)
				if err != nil {
					return err
				}
			}
			if decision != "" && expected == digest {
				matched = true
				if decision == "decline" {
					err = declineCodexReviewPolicy(store, proposal)
					declined = true
				} else if declined {
					return fmt.Errorf("explicit policy decline is preserved; revisit the decision before allowing it")
				} else {
					err = installCodexReviewPolicy(consent)
				}
				if err != nil {
					return err
				}
				proposal, _, err = inspectCodexReviewPolicy(consent)
				if err != nil {
					return err
				}
			}
			if declined {
				proposal.State = "declined"
			}
			if proposal.State != "configured" && !declined {
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				base := []string{exe, "--api-url", api, "--profile", profile, "watch", "codex-policy", "--manager-home", home, "--proposal", digest}
				proposal.InstallCommand = append(append([]string{}, base...), "--decision", "allow")
				proposal.DeclineCommand = append(append([]string{}, base...), "--decision", "decline")
			}
			proposals = append(proposals, proposal)
		}
		if decision != "" && !matched {
			return fmt.Errorf("policy scope or configuration changed; inspect and approve the new proposal")
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"policies": proposals, "activation": "unverified"})
	}
	return c
}
