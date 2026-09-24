package githubreview

import (
	"context"
	"testing"
)

func duplicateTestPlan() CommentPlan {
	line := 305
	return CommentPlan{
		Comments: []PlannedComment{
			{
				FindingID: "range-not-honored", Adversary: "review/code", Severity: "medium",
				Title: "7d selector shows 30 days", Summary: "The new 7d selector fetches 30-day data because normalizeAnalyticsDays only accepts 90.",
				Recommendation: "Allow 7 in normalizeAnalyticsDays.", Anchor: Anchor{Path: "components/run-analytics.tsx", Line: &line}, Placement: "inline",
			},
			{
				FindingID: "inactive-range", Adversary: "review/code", Severity: "medium",
				Title: "7d link is dead on arrival", Summary: "range=7 collapses to 30 in normalizeAnalyticsDays, so the 7d tab is never active.",
				Recommendation: "Accept 7 in normalizeAnalyticsDays or remove the selector.", Anchor: Anchor{Path: "components/run-analytics.tsx", Line: &line}, Placement: "inline",
			},
		},
		ReviewBody: "Adversary found 2 actionable issues",
		Summary:    PlanSummary{FindingsSeen: 2, Comments: 2, Inline: 2},
	}
}

func TestDeduplicateCommentsCollapsesParaphrasedSameIssue(t *testing.T) {
	plan := duplicateTestPlan()
	provider := &matchProvider{decision: "same"}
	DeduplicateComments(context.Background(), &plan, provider)
	if provider.calls != 1 || len(plan.Comments) != 1 || plan.Comments[0].FindingID != "range-not-honored" {
		t.Fatalf("comments=%+v calls=%d", plan.Comments, provider.calls)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0].FindingID != "inactive-range" || plan.Skipped[0].Reason != "duplicate_issue" {
		t.Fatalf("skipped=%+v", plan.Skipped)
	}
	if plan.Summary.FindingsSeen != 2 || plan.Summary.Comments != 1 || plan.Summary.Inline != 1 || plan.Summary.Skipped != 1 ||
		plan.ReviewBody != TemplateSummary(plan.Comments) {
		t.Fatalf("summary=%+v body=%q", plan.Summary, plan.ReviewBody)
	}
}

func TestDeduplicateCommentsKeepsDistinctIssueAtSameLine(t *testing.T) {
	plan := duplicateTestPlan()
	plan.Comments[1].Summary = "This component renders an unsafe external URL that can redirect users."
	plan.Comments[1].Recommendation = "Validate the destination host."
	provider := &matchProvider{decision: "different"}
	DeduplicateComments(context.Background(), &plan, provider)
	if provider.calls != 1 || len(plan.Comments) != 2 || len(plan.Skipped) != 0 {
		t.Fatalf("plan=%+v calls=%d", plan, provider.calls)
	}
}

func TestDeduplicateCommentsWithoutModelOnlyCollapsesIdenticalClaim(t *testing.T) {
	plan := duplicateTestPlan()
	DeduplicateComments(context.Background(), &plan, nil)
	if len(plan.Comments) != 2 {
		t.Fatalf("paraphrases suppressed without evidence: %+v", plan.Comments)
	}
	plan.Comments[1].Summary = plan.Comments[0].Summary
	plan.Comments[1].Recommendation = "Use a different correction."
	DeduplicateComments(context.Background(), &plan, nil)
	if len(plan.Comments) != 2 {
		t.Fatalf("conflicting corrections suppressed without a model: %+v", plan.Comments)
	}
	plan.Comments[1].Recommendation = plan.Comments[0].Recommendation
	DeduplicateComments(context.Background(), &plan, nil)
	if len(plan.Comments) != 1 || len(plan.Skipped) != 1 {
		t.Fatalf("identical claim not collapsed: %+v", plan)
	}
}

func TestDeduplicateCommentsKeepsStrongestAndRequiresNearbyLocation(t *testing.T) {
	plan := duplicateTestPlan()
	plan.Comments[1].Severity = "high"
	provider := &matchProvider{decision: "same"}
	DeduplicateComments(context.Background(), &plan, provider)
	if len(plan.Comments) != 1 || plan.Comments[0].FindingID != "inactive-range" || plan.Skipped[0].FindingID != "range-not-honored" {
		t.Fatalf("higher-severity finding not retained: %+v", plan)
	}
	plan = duplicateTestPlan()
	farLine := 500
	plan.Comments[1].Anchor.Line = &farLine
	DeduplicateComments(context.Background(), &plan, provider)
	if len(plan.Comments) != 2 || provider.calls != 1 {
		t.Fatalf("unrelated location compared or dropped: %+v calls=%d", plan, provider.calls)
	}
}
