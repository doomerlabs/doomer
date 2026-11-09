package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

// A local receipt is useful context for host review, not proof of the current
// account or permission to transmit. Only online preparation establishes that.
type preflightConsent struct {
	Consent            *managedConsentEvidence `json:"consent"`
	CodexPolicyPreview *codexReviewPolicy      `json:"codex_review_policy_preview,omitempty"`
	CodexPolicyStatus  *codexPolicyStatus      `json:"codex_policy_status,omitempty"`
	ClaudePermissions  *claudePermissions      `json:"claude_permissions,omitempty"`
}
type preflightResult struct {
	ClaudePermissions *claudePermissions `json:"claude_permissions,omitempty"`
	State             string             `json:"state"`
	LocalOnly         bool               `json:"local_only"`
	AccountVerified   bool               `json:"account_verified"`
	Repository        string             `json:"repository"`
	RepositoryID      string             `json:"repository_id"`
	SavedConsents     []preflightConsent `json:"saved_consents"`
	Action            string             `json:"action"`
}

func newPreflightCommand(api, profile *string) *cobra.Command {
	repo, agent := ".", "codex"
	var declineAccount string
	if os.Getenv("CLAUDECODE") != "" {
		agent = "claude"
	}
	c := &cobra.Command{Use: "preflight", Short: "Inspect review readiness locally or explicitly record a policy decline", Args: cobra.NoArgs}
	c.RunE = func(c *cobra.Command, _ []string) error {
		if agent != "codex" && agent != "claude" {
			return fmt.Errorf("agent must be codex or claude")
		}
		root, err := watch.Root(c.Context(), repo)
		if err != nil {
			return err
		}
		id, err := watch.RepositoryIdentity(c.Context(), root)
		if err != nil {
			return err
		}
		s, err := managedStoreLocation()
		if err != nil {
			return err
		}
		// Inspection does not initialize state, migrate credentials, restore portable
		// state, resolve a runtime or emit telemetry. Only the explicit decline
		// flag below writes a local decision; it never changes agent settings.
		accounts := map[string]bool{}
		for _, dir := range []string{"integration-consents", "consents"} {
			files, err := filepath.Glob(filepath.Join(s.Root, dir, "*.json"))
			if err != nil {
				return err
			}
			for _, path := range files {
				var grant repositoryConsent
				if err := watch.Read(path, &grant); err != nil {
					return err
				}
				task := managed.Task{Repo: root, RepositoryID: id, API: *api, Profile: *profile, Agent: agent, AccountID: grant.Account}
				if grant.Account != "" && grant.matches(task) {
					accounts[grant.Account] = true
				}
			}
		}
		names := make([]string, 0, len(accounts))
		for account := range accounts {
			names = append(names, account)
		}
		sort.Strings(names)
		result := preflightResult{State: "no_saved_consent", LocalOnly: true, Repository: root, RepositoryID: id, SavedConsents: []preflightConsent{}, Action: "Run normal prepare to verify the current account and consent. For a sandbox restriction, request ordinary host approval for private-state writes and account checks, citing saved scope as context; respect explicit reviewer denials. Local preflight never authorizes an upload or changes settings. If review is blocked, continue authorized local work unless the user explicitly required review first."}
		if agent == "claude" && len(names) == 0 {
			result.ClaudePermissions = inspectClaudePermissions()
			if result.ClaudePermissions.State == "missing" {
				result.Action += " Claude readiness permissions are missing. Offer account verification and normal onboarding before prepare; obtain live upload consent before installing scoped settings."
			}
		}
		claudeRepairOffered := false
		for _, account := range names {
			evidence, err := savedManagedConsentEvidence(s, managed.Task{Repo: root, RepositoryID: id, API: *api, Profile: *profile, Agent: agent, AccountID: account})
			if err != nil {
				return err
			}
			if evidence == nil {
				continue
			}
			if declineAccount != "" && account != declineAccount {
				continue
			}
			if declineAccount != "" {
				if agent == "codex" {
					proposal, _, err := inspectCodexReviewPolicy(*evidence)
					if err != nil {
						return err
					}
					if err := declineCodexReviewPolicy(s, proposal); err != nil {
						return err
					}
				} else {
					path, err := claudePermissionDecisionPath(s, managed.Task{Agent: agent}, evidence)
					if err != nil {
						return err
					}
					if err := watch.Write(path, "declined"); err != nil {
						return err
					}
				}
			}
			binary, err := os.Executable()
			if err != nil {
				return err
			}
			declineCommand := []string{binary, "--api-url", *api, "--profile", *profile, "preflight", "--agent", agent, "--repo", root, "--decline-policy", account}
			entry := preflightConsent{Consent: evidence}
			if agent == "codex" {
				proposal, status := inspectCodexPolicy(*evidence)
				entry.CodexPolicyStatus = status
				if status.State == "missing" {
					declined, decisionErr := codexReviewPolicyDeclined(s, proposal)
					if decisionErr != nil {
						status.DecisionError = decisionErr.Error()
					} else if declined {
						status.Decision = "declined"
					} else {
						status.Decision = "pending"
					}
					if status.Decision == "pending" {
						entry.CodexPolicyPreview = proposal
						proposal.DeclineCommand = declineCommand
						proposal.Action = "Local preview only: present the full extra_policy and changes, including account, service, profile, agent and all-repositories versus repository-only scope, before asking the user to approve verification and installation. This is not verified upload consent. After acceptance, run prepare, verify live consent and compare the returned policy with this preview before installing. If the scope differs, obtain approval for the changed proposal. If declined, execute decline_command to record the local choice without preparing a task or contacting the service."
						result.Action += " Codex reviewer policy is missing. Before prepare, show codex_review_policy_preview in full, including its all-repositories or repository-only scope, and ask to verify the current account and install that exact policy. After acceptance run prepare and install only after live consent verification. This settings choice is separate from upload consent."
					}
				}
			} else {
				entry.ClaudePermissions = proposedClaudePermissions(s, managed.Task{Agent: agent, API: *api, Profile: *profile}, evidence)
				entry.ClaudePermissions.InstallCommand = nil
				entry.ClaudePermissions.DeclineCommand = nil
				if entry.ClaudePermissions.Decision == "pending" {
					entry.ClaudePermissions.DeclineCommand = declineCommand
					entry.ClaudePermissions.Action = "On an explicit refusal, execute decline_command to record the local choice without a task or network call. Otherwise offer verification and scoped settings repair before prepare. After acceptance, verify live consent and use the returned task proposal to install. This local inspection does not authorize uploads or settings changes."
				}
				if entry.ClaudePermissions.State == "missing" && entry.ClaudePermissions.Decision == "pending" && !claudeRepairOffered {
					claudeRepairOffered = true
					result.Action += " Claude readiness permissions are missing. Before running prepare, offer to verify the current account and repair the displayed Claude permissions. After acceptance run prepare, then install only after live consent verification. This is a local settings decision, not a new repository upload grant. Do not bypass explicit deny rules."
				}
			}
			result.SavedConsents = append(result.SavedConsents, entry)
		}
		if len(result.SavedConsents) > 0 {
			result.State = "saved_consent_found"
		}
		if declineAccount != "" {
			if len(result.SavedConsents) == 0 {
				return fmt.Errorf("no matching local consent for the selected policy decline")
			}
			result.State = "reviewer_policy_declined"
			result.Action = "Recorded the explicit local reviewer-policy decline. Preserve the upload grant and agent settings; do not offer this same setup again unless the user revisits the decision. No network call or task was required."
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(result)
	}
	c.Flags().StringVar(&repo, "repo", repo, "repository to inspect")
	c.Flags().StringVar(&agent, "agent", agent, "coding agent: codex or claude")
	c.Flags().StringVar(&declineAccount, "decline-policy", "", "record an explicit local reviewer-policy decline for this saved account")
	return c
}

func inspectCodexPolicyStatus(consent managedConsentEvidence) *codexPolicyStatus {
	_, status := inspectCodexPolicy(consent)
	return status
}

func inspectCodexPolicy(consent managedConsentEvidence) (*codexReviewPolicy, *codexPolicyStatus) {
	p, _, err := inspectCodexReviewPolicy(consent)
	status := &codexPolicyStatus{State: "unreadable", Activation: "unverified", Action: "Configuration inspection cannot verify the running Codex reviewer. After an accepted policy installation, reload or restart Codex and verify an ordinary permitted Doomer operation in a fresh session; retain host and organization restrictions."}
	if err != nil {
		status.Error = err.Error()
	} else {
		status.ConfigPath, status.State = p.ConfigPath, p.State
	}
	return p, status
}
