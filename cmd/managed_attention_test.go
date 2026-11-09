package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
)

func TestPrepareAttentionKeepsIndependentDecisionsVisible(t *testing.T) {
	r := managedResult{State: "ready", UploadApproved: true, CodexReviewPolicy: &codexReviewPolicy{State: "missing"}, BootstrapUpdates: []managed.BootstrapUpdate{{State: "update_available", Installed: "2026.11.8.12", Available: "2026.11.9.1"}}, Instructions: strings.Repeat("review instructions ", 1000)}
	prepareAttention(&r)
	if !r.AttentionRequired || !r.UploadApproved || r.State != "setup_decision_required" {
		t.Fatalf("changed readiness or lost pending decisions: %+v", r)
	}
	for _, want := range []string{"missing Codex reviewer policy", "2026.11.8.12", "2026.11.9.1", "older PR does not cover", "only after acceptance"} {
		if !strings.Contains(r.Action, want) {
			t.Fatalf("missing %q in action %q", want, r.Action)
		}
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(b), `"action"`) > strings.Index(string(b), `"instructions"`) {
		t.Fatal("pending decisions follow long instructions")
	}
}

func TestPrepareAttentionDoesNotReofferResolvedDecisions(t *testing.T) {
	r := managedResult{State: "ready", Action: "Continue review", BootstrapUpdates: []managed.BootstrapUpdate{{State: "current"}, {State: "locally_modified"}}}
	prepareAttention(&r)
	if r.AttentionRequired || r.Action != "Continue review" {
		t.Fatal("invented a pending decision", r.Action)
	}
}

func TestPrepareAttentionPreservesOtherOperationOutcomes(t *testing.T) {
	for _, state := range []string{"network_access_required", "approval_required", "review_pending", "reviewed", "complete"} {
		r := managedResult{State: state, Action: "Recover existing operation", CodexReviewPolicy: &codexReviewPolicy{State: "missing"}, Instructions: "Existing recovery instructions"}
		prepareAttention(&r)
		if r.State != state || r.Action != "Recover existing operation" || r.Instructions != "Existing recovery instructions" || r.AttentionRequired {
			t.Fatalf("replaced %s recovery: %+v", state, r)
		}
	}
}
