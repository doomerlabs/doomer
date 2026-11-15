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

func TestCodexReviewPolicyIncludesFindingDecisionsAndExplanations(t *testing.T) {
	policy := proposedCodexReviewPolicy(managedConsentEvidence{Account: "account", Service: "https://example.test/api", Profile: "default", Agent: "codex", AllRepositories: true}).ExtraPolicy
	for _, want := range []string{"watch findings respond", "fixed, dismissed, and deferred", "repository-specific", "explanations or evidence", "separate approval for each review or response"} {
		if !strings.Contains(policy, want) {
			t.Fatalf("missing response authorization: %s", want)
		}
	}
}

func TestCodexReviewPolicyMigratesExactPreviousResponseWording(t *testing.T) {
	consent := managedConsentEvidence{Account: "account", Service: "https://example.test/api", Profile: "default", Agent: "codex", AllRepositories: true}
	old := codexReviewPolicyText(consent, false).ExtraPolicy
	got := mergeCodexExtraPolicy("Keep unrelated policy.\n"+old, consent)
	if strings.Count(got, "Doomer repository review authorization") != 1 || !strings.Contains(got, "Keep unrelated policy.") || !strings.Contains(got, "watch findings respond") {
		t.Fatal("migration must replace only exact old policy")
	}
	edited := strings.Replace(old, "Finding responses", "Custom finding responses", 1)
	if !strings.Contains(mergeCodexExtraPolicy(edited, consent), edited) {
		t.Fatal("custom policy was removed")
	}
}

func TestCodexReviewPolicyRetainsDeclineAfterWordingUpgrade(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	store := managed.Store{Root: t.TempDir()}
	consent := managedConsentEvidence{Account: "account", Service: "https://example.test/api", Profile: "default", Agent: "codex", AllRepositories: true}
	current, _, err := inspectCodexReviewPolicy(consent)
	if err != nil {
		t.Fatal(err)
	}
	previous := *current
	previous.ExtraPolicy = codexReviewPolicyText(consent, false).ExtraPolicy
	if err := declineCodexReviewPolicy(store, &previous); err != nil {
		t.Fatal(err)
	}
	declined, err := codexReviewPolicyDeclined(store, current)
	if err != nil || !declined {
		t.Fatalf("prior explicit decline lost: %v", err)
	}
}

func TestPreviousPolicyTextMatchesReleasedPolicy(t *testing.T) {
	consent := managedConsentEvidence{Account: "account", Service: "https://example.test/api", Profile: "default", Agent: "codex", AllRepositories: true}
	got := codexReviewPolicyText(consent, false).ExtraPolicy
	if strings.Contains(got, "review.  This") || !strings.Contains(got, "and finding responses for review. This covers") {
		t.Fatal("previous policy spacing changed")
	}
	if strings.Replace(proposedCodexReviewPolicy(consent).ExtraPolicy, codexFindingResponseDetails, "", 1) != got {
		t.Fatal("previous policy reconstruction differs")
	}
}

func TestCodexReviewPolicyDoesNotAuthorizeOtherDestinations(t *testing.T) {
	policy := proposedCodexReviewPolicy(managedConsentEvidence{Account: "account", Service: "https://example.test/api", Profile: "default", Agent: "codex", AllRepositories: true}).ExtraPolicy
	for _, restriction := range []string{"other destinations", "credential disclosure", "Respect revoked consent", "Keep host and organization restrictions in force"} {
		if !strings.Contains(policy, restriction) {
			t.Fatalf("missing scope restriction: %s", restriction)
		}
	}
}
