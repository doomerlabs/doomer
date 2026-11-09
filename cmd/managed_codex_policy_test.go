package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
)

func TestCodexReviewPolicyKeepsScopeAsData(t *testing.T) {
	consent := managedConsentEvidence{Account: "account", Repository: "/repo/\"quoted\"\npath/.git", Service: "https://doomer.ai/api", Profile: "work"}
	proposal := proposedCodexReviewPolicy(consent)
	if len(proposal.Changes) != 2 {
		t.Fatal("settings changes were not disclosed", proposal)
	}
	policy := proposal.ExtraPolicy
	lines := strings.Split(policy, "\n")
	var scope map[string]string
	if err := json.Unmarshal([]byte(lines[2]), &scope); err != nil {
		t.Fatal(err)
	}
	if scope["repository"] != consent.Repository || scope["account"] != consent.Account || scope["service"] != consent.Service || scope["profile"] != consent.Profile {
		t.Fatal(scope)
	}
	for _, text := range []string{"prepare or resume", "private task", "finding responses", "current CLI consent evidence", "revoked consent", "paused tasks", "organization restrictions", "credential disclosure"} {
		if !strings.Contains(policy, text) {
			t.Fatalf("policy missing %q", text)
		}
	}
}

func TestCodexReviewPolicyOnlyExplicitDeclineSuppressesProposal(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	s := managed.Store{Root: t.TempDir()}
	consent := managedConsentEvidence{Account: "one", Repository: "/repo/.git", Service: "https://doomer.ai/api", Profile: "default"}
	first, err := offerCodexReviewPolicy(s, consent)
	if err != nil || first == nil {
		t.Fatal(first, err)
	}
	// Merely inspecting or emitting a proposal must not suppress later prompts.
	undelivered, err := offerCodexReviewPolicy(s, consent)
	if err != nil || undelivered == nil {
		t.Fatal("unanswered proposal was lost", undelivered, err)
	}
	if err := declineCodexReviewPolicy(s, first); err != nil {
		t.Fatal(err)
	}
	// A new Store represents a later CLI process/chat.
	later, err := offerCodexReviewPolicy(managed.Store{Root: s.Root}, consent)
	if err != nil || later != nil {
		t.Fatal("explicit decline was ignored", later, err)
	}
	consent.Account = "two"
	other, err := offerCodexReviewPolicy(s, consent)
	if err != nil || other == nil {
		t.Fatal("new account lost its scoped offer", other, err)
	}
}

func TestIntegrationPolicyIsStableAcrossRepositories(t *testing.T) {
	consent := managedConsentEvidence{Account: "account", Agent: "codex", AllRepositories: true, Service: "https://doomer.ai/api", Profile: "default"}
	policy := proposedCodexReviewPolicy(consent).ExtraPolicy
	var scope map[string]any
	if err := json.Unmarshal([]byte(strings.Split(policy, "\n")[2]), &scope); err != nil {
		t.Fatal(err)
	}
	if scope["all_repositories"] != true || scope["agent"] != "codex" || scope["repository"] != nil {
		t.Fatal(scope)
	}
	if !strings.Contains(strings.Join(proposedCodexReviewPolicy(consent).Changes, " "), "repository-scoped Doomer policy text") {
		t.Fatal("replacement not disclosed")
	}
	if !strings.Contains(policy, "all current and future repositories") {
		t.Fatal(policy)
	}
}
