package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
)

func TestCodexReviewPolicyKeepsScopeAsData(t *testing.T) {
	consent := managedConsentEvidence{Account: "account", Repository: "/repo/\"quoted\"\npath/.git", Service: "https://doomer.ai/api", Profile: "work"}
	policy := proposedCodexReviewPolicy(consent).ExtraPolicy
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

func TestCodexReviewPolicyOfferPersistsWithoutGrantingSettings(t *testing.T) {
	s := managed.Store{Root: t.TempDir()}
	consent := managedConsentEvidence{Account: "one", Repository: "/repo/.git", Service: "https://doomer.ai/api", Profile: "default"}
	first, err := offerCodexReviewPolicy(s, consent)
	if err != nil || first == nil {
		t.Fatal(first, err)
	}
	// A new Store represents a later CLI process/chat.
	later, err := offerCodexReviewPolicy(managed.Store{Root: s.Root}, consent)
	if err != nil || later != nil {
		t.Fatal("repeated optional settings prompt", later, err)
	}
	consent.Account = "two"
	other, err := offerCodexReviewPolicy(s, consent)
	if err != nil || other == nil {
		t.Fatal("new account lost its scoped offer", other, err)
	}
}
