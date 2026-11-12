package cmd

import (
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/livewatch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestFeedbackHookRequestsEveryUnansweredFinding(t *testing.T) {
	response := livewatch.Response{Findings: []livewatch.Finding{{ReviewID: "review", Lead: adversarylabs.WorkspaceLead{ID: strings.Repeat("a", 64), Path: "file.go", Line: 12, Concern: "Check the guard"}}}}
	out := pendingFeedbackHook(response, strings.Repeat("b", 64))
	if out["decision"] != "block" {
		t.Fatal(out)
	}
	text := out["reason"].(string)
	for _, want := range []string{"watch respond", "review_id", "lead_id", "status=fixed", "dismissed", "deferred", "Do not dump"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	response.Findings[0].Disposition = &livewatch.Feedback{Status: "dismissed"}
	if pendingFeedbackHook(response, "") != nil {
		t.Fatal("asked for recorded response again")
	}
}

func TestFinalFeedbackHookSummarizesCountsAndActualFixes(t *testing.T) {
	response := livewatch.Response{}
	for i, status := range []string{"fixed", "fixed", "dismissed"} {
		response.Findings = append(response.Findings, livewatch.Finding{ReviewID: "review", Repository: "/repo", URL: "https://doomer.ai/review/example", Lead: adversarylabs.WorkspaceLead{Path: "file.go", Line: i + 1, Concern: "guard"}, Disposition: &livewatch.Feedback{Status: status, Reason: "Verified change"}})
	}
	out := finalFeedbackHook(response)
	text := out["reason"].(string)
	for _, want := range []string{"reported 3", "fixed 2", "disagreed with 1", "deferred 0", "Fixed findings:", "Verified change", "https://doomer.ai/review/example", "Do not repeat the counts"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	response.Findings = response.Findings[2:]
	text = finalFeedbackHook(response)["reason"].(string)
	if !strings.Contains(text, "made no changes based on them") || strings.Contains(text, "Fixed findings:") {
		t.Fatal(text)
	}
	response.Findings[0].Disposition.Status = "deferred"
	text = finalFeedbackHook(response)["reason"].(string)
	if !strings.Contains(text, "deferred 1") || !strings.Contains(text, "disclose any deferred unresolved concern") {
		t.Fatal(text)
	}
}
