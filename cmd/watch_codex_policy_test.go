package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/livewatch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestNodePolicyConsentScopeAndDecline(t *testing.T) {
	isolate(t)
	t.Chdir(t.TempDir())
	home, config := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", config)
	t.Setenv("DOOMER_TOKEN", "doomer_anon_"+strings.Repeat("a", 43))
	t.Setenv("DOOMER_PROFILE", "work")
	credentials := t.TempDir()
	t.Setenv("DOOMER_CONFIG_DIR", credentials)
	original := "# keep comment\nmodel = 'keep-model'\n[auto_review]\nextra_policy = 'Keep unrelated authorization'\n"
	os.WriteFile(filepath.Join(config, "config.toml"), []byte(original), 0600)
	api := "https://doomer.ai/api"
	raw, _ := json.Marshal(map[string]any{"api": api, "grants": []any{map[string]any{"scope": "global", "api": api, "approvedAt": time.Now()}}})
	os.WriteFile(filepath.Join(home, "state.json"), raw, 0600)
	inspect := func() *codexReviewPolicy {
		out, err := execute(t, "", "watch", "codex-policy", "--manager-home", home)
		if err != nil {
			t.Fatal(out, err)
		}
		var result struct {
			Policies []*codexReviewPolicy `json:"policies"`
		}
		if err = json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(out, err)
		}
		return result.Policies[0]
	}
	proposal := inspect()
	for _, want := range []string{`"profile":"work"`, `"all_repositories":true`, `"agent":"codex"`, credentials, `"account":"anonymous:`} {
		if !strings.Contains(proposal.ExtraPolicy, want) {
			t.Fatalf("missing scope %s: %s", want, proposal.ExtraPolicy)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(config, "config.toml")); string(data) != original {
		t.Fatal("inspection mutated config")
	}
	if out, err := execute(t, "", proposal.DeclineCommand[1:]...); err != nil {
		t.Fatal(out, err)
	}
	if got := inspect(); got.State != "declined" || len(got.InstallCommand) != 0 {
		t.Fatal("decline lost", got)
	}
	if out, err := execute(t, "", proposal.InstallCommand[1:]...); err == nil {
		t.Fatal("overrode decline", out)
	}
	t.Setenv("DOOMER_PROFILE", "other")
	proposal = inspect()
	if out, err := execute(t, "", proposal.InstallCommand[1:]...); err != nil {
		t.Fatal(out, err)
	}
	data, _ := os.ReadFile(filepath.Join(config, "config.toml"))
	for _, want := range []string{"# keep comment", "model = 'keep-model'", "Keep unrelated authorization"} {
		if !strings.Contains(string(data), want) {
			t.Fatal("lost unrelated config", want)
		}
	}
	if inspect().State != "configured" {
		t.Fatal("not installed")
	}
	// Changed config invalidates an earlier accepted proposal instead of expanding consent.
	os.WriteFile(filepath.Join(config, "config.toml"), []byte(original), 0600)
	t.Setenv("DOOMER_PROFILE", "another")
	proposal = inspect()
	os.WriteFile(filepath.Join(config, "config.toml"), []byte(strings.Replace(original, "authorization", "policy", 1)), 0600)
	if _, err := execute(t, "", proposal.InstallCommand[1:]...); err == nil {
		t.Fatal("accepted stale proposal")
	}
}

func TestNodePolicyMergePreservesOtherCredentialDirectory(t *testing.T) {
	a := managedConsentEvidence{Account: "account", Service: "service", Profile: "default", CredentialDirectory: "/first", Agent: "codex", AllRepositories: true}
	b := a
	b.CredentialDirectory = "/second"
	b.AllRepositories = false
	b.Repository = "repo"
	policy := mergeCodexExtraPolicy(proposedCodexReviewPolicy(a).ExtraPolicy, b)
	if !strings.Contains(policy, proposedCodexReviewPolicy(a).ExtraPolicy) || !strings.Contains(policy, proposedCodexReviewPolicy(b).ExtraPolicy) {
		t.Fatal("merged distinct credential scopes")
	}
}

func TestCodexPendingFeedbackDoesNotClaimCompletion(t *testing.T) {
	result := livewatch.Response{Findings: []livewatch.Finding{{ReviewID: "r", Lead: adversarylabs.WorkspaceLead{ID: "l"}}}}
	if codexStopFeedback(result, false)["decision"] != "block" {
		t.Fatal("first notification missing")
	}
	for i := 0; i < 3; i++ {
		if out := codexStopFeedback(result, true); out["decision"] != nil || out["systemMessage"] == nil {
			t.Fatal("repeated blocking notification", out)
		}
	}
	if makeFeedbackReport(result).Complete {
		t.Fatal("pending finding reported complete")
	}
}

func TestWatchReportCarriesVerifiedConsentWithoutCredentials(t *testing.T) {
	evidence := &livewatch.ConsentEvidence{Account: "account", Service: "https://example.test/api", Profile: "work", CredentialDirectory: "/credentials", Agent: "codex", AllRepositories: true, Includes: []string{"finding_responses"}}
	result := livewatch.Response{Consent: evidence, Findings: []livewatch.Finding{{ReviewID: "review", Lead: adversarylabs.WorkspaceLead{ID: "lead", Path: "hello.py", Line: 1}}}}
	raw, err := json.Marshal(makeFeedbackReport(result))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"consent"`, `"account":"account"`, `"profile":"work"`, `"finding_responses"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatal("missing evidence", string(raw))
		}
	}
	if text := feedbackInstructions(result, strings.Repeat("a", 64)); !strings.Contains(text, "Current CLI-verified upload consent") || !strings.Contains(text, "credential_directory") {
		t.Fatal(text)
	}
}

func TestNodePolicyPreservesRepositoryGrant(t *testing.T) {
	isolate(t)
	t.Chdir(t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("DOOMER_TOKEN", "doomer_anon_"+strings.Repeat("b", 43))
	t.Setenv("DOOMER_PROFILE", "team")
	repo := newWatchTestRepo(t)
	home := t.TempDir()
	api := "https://example.test/api"
	raw, _ := json.Marshal(map[string]any{"api": api, "grants": []any{map[string]any{"repo": repo, "agents": []string{"codex"}, "api": api, "approvedAt": time.Now()}}})
	if err := os.WriteFile(filepath.Join(home, "state.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "", "watch", "codex-policy", "--manager-home", home)
	if err != nil {
		t.Fatal(out, err)
	}
	var result struct {
		Policies []codexReviewPolicy `json:"policies"`
	}
	if err = json.Unmarshal([]byte(out), &result); err != nil || len(result.Policies) != 1 {
		t.Fatal(out, err)
	}
	policy := result.Policies[0].ExtraPolicy
	if strings.Contains(policy, `"all_repositories":true`) || !strings.Contains(policy, `"repository":`) || !strings.Contains(policy, `"profile":"team"`) || !strings.Contains(policy, api) {
		t.Fatal("broadened or changed grant", policy)
	}
	after, _ := os.ReadFile(filepath.Join(home, "state.json"))
	if string(after) != string(raw) {
		t.Fatal("policy inspection changed upload grant")
	}
}
