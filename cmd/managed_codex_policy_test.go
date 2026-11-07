package cmd

import (
	"encoding/json"
	"strings"
	"testing"
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
