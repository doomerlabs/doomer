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
	visible := out["reason"].(string)
	if !strings.Contains(visible, "watch instructions") {
		t.Fatal(visible)
	}
	for _, hidden := range []string{"review_id", "lead_id", "status=fixed", "Do not dump", "internal handoff"} {
		if strings.Contains(visible, hidden) {
			t.Fatalf("verbose guidance leaked into hook reason: %s", visible)
		}
	}
	text := feedbackInstructions(response, strings.Repeat("b", 64))
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
	visible := out["reason"].(string)
	for _, hidden := range []string{"internal handoff", "Do not repeat", "Fixed findings:", "Verified change"} {
		if strings.Contains(visible, hidden) {
			t.Fatalf("instructions leaked into visible summary: %s", visible)
		}
	}
	if !strings.Contains(visible, "2 suggestion(s) were fixed") || !strings.Contains(visible, "https://doomer.ai/review/example") {
		t.Fatal(visible)
	}
	text := feedbackReport(response)
	for _, want := range []string{"reported 3", "fixed 2", "disagreed with 1", "deferred 0", "Fixed findings:", "Verified change", "https://doomer.ai/review/example", "Do not repeat the counts"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	response.Findings = response.Findings[2:]
	text = finalFeedbackHook(response)["reason"].(string)
	if !strings.Contains(text, "no changes were made based on them") || strings.Contains(text, "Fixed findings:") {
		t.Fatal(text)
	}
	response.Findings[0].Disposition.Status = "deferred"
	text = finalFeedbackHook(response)["reason"].(string)
	if !strings.Contains(text, "1 suggestion(s) remain deferred") {
		t.Fatal(text)
	}
}

func TestPartialFeedbackReportDoesNotClaimCompletion(t *testing.T) {
	report := feedbackReport(livewatch.Response{Findings: []livewatch.Finding{{Lead: adversarylabs.WorkspaceLead{ID: "pending"}}}})
	if !strings.Contains(report, "Unanswered: 1") || strings.Contains(report, "No more response commands") {
		t.Fatal(report)
	}
}
