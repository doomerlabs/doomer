package cmd

import (
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestLookCloserResultPresentation(t *testing.T) {
	link := "https://doomer.test/look-closer/39593a10-37f7-4d82-bf3f-aab643ffe3c5#private-capability"
	for _, tc := range []struct {
		name, status, want string
		leads              []adversarylabs.WorkspaceLead
	}{
		{name: "empty", status: "ready", want: "✓ No areas found that need a closer look."},
		{name: "one", status: "ready", want: "→ 1 area needs a closer look", leads: []adversarylabs.WorkspaceLead{{Concern: "Retries can duplicate a request", Path: "src/client.ts", Line: 84}}},
		{name: "two", status: "ready", want: "→ 2 areas need a closer look", leads: []adversarylabs.WorkspaceLead{{Concern: "First concern", Path: "first.go", Line: 2}, {Concern: "Second concern", Path: "second.go", Line: 3}}},
		{name: "pending", status: "pending", want: "Look Closer: pending."},
		{name: "unavailable", status: "unavailable", want: "Look Closer: unavailable."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review := adversarylabs.WorkspaceReview{Anonymous: true, Early: &adversarylabs.WorkspaceAssessment{Status: tc.status, Leads: tc.leads}, Full: &adversarylabs.WorkspaceFullReview{Enabled: false, Status: "disabled"}}
			out, err := workspaceReviewText(review, link, false)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "RESULTS · LOOK CLOSER") || !strings.Contains(out, tc.want) || strings.Count(out, link) != 1 || strings.Contains(out, "Deep review: disabled") || strings.Contains(out, "\x1b") {
				t.Fatal(out)
			}
			if tc.status != "ready" && strings.Contains(out, "No areas found") {
				t.Fatal("incomplete review appeared to have no concerns")
			}
			if len(tc.leads) == 1 && strings.Contains(out, "\n  1. ") {
				t.Fatal("single result redundantly numbered")
			}
			if len(tc.leads) > 1 && (!strings.Contains(out, "\n  1. ") || !strings.Contains(out, "\n  2. ")) {
				t.Fatal("multiple results lost their numbering")
			}
			for i, lead := range tc.leads {
				if !strings.Contains(out, lead.Concern) || !strings.Contains(out, lead.Path) {
					t.Fatalf("missing lead %d: %s", i+1, out)
				}
			}
		})
	}
}

func TestReviewLinksHideCapabilityOnlyInTerminal(t *testing.T) {
	link := "https://doomer.test/look-closer/id#secret"
	terminal := reviewLinkLabel(link, true)
	if terminal != "\x1b]8;;"+link+"\x1b\\Open review ↗\x1b]8;;\x1b\\" {
		t.Fatal("missing clickable terminal link")
	}
	if plain := reviewLinkLabel(link, false); plain != "Open review: "+link {
		t.Fatal("redirected output must retain a usable URL")
	}
	for _, unsafe := range []string{"https://example.test/\x1b]bad", "file:///tmp/secret", "https://user:pass@example.test/"} {
		if got := reviewLinkLabel(unsafe, true); got != "Review link unavailable" {
			t.Fatal("unsafe terminal link accepted")
		}
	}
}

func TestDeepOnlyReviewDoesNotInventLookCloserResults(t *testing.T) {
	disabled := false
	review := adversarylabs.WorkspaceReview{LookCloserEnabled: &disabled, Full: &adversarylabs.WorkspaceFullReview{Enabled: true, Status: "running"}}
	out, err := workspaceReviewText(review, "https://doomer.test/review", false)
	if err != nil || !strings.Contains(out, "RESULTS · DEEP REVIEW") || strings.Contains(out, "LOOK CLOSER") || !strings.Contains(out, "running") {
		t.Fatal(out, err)
	}
	review.Full.Result = &adversarylabs.ReviewResult{Conclusion: "success", Summary: "Deep review finished"}
	out, err = workspaceReviewText(review, "https://doomer.test/review", false)
	if err != nil || !strings.Contains(out, "Deep review finished") {
		t.Fatal(out, err)
	}
}

func TestReviewUsesShortContentReference(t *testing.T) {
	if got := shortReviewDigest("8f3c2a123456789"); got != "8f3c2a1" {
		t.Fatal(got)
	}
	if got := shortReviewDigest(""); got != "working tree" {
		t.Fatal(got)
	}
}

func TestDisabledDeepReviewIsNotDisplayedAsAnError(t *testing.T) {
	review := adversarylabs.WorkspaceReview{Error: "full_review_disabled", Early: &adversarylabs.WorkspaceAssessment{Status: "ready"}, Full: &adversarylabs.WorkspaceFullReview{Enabled: false, Status: "disabled"}}
	out, err := workspaceReviewText(review, "https://doomer.test/review", false)
	if err != nil || strings.Contains(out, "full_review_disabled") {
		t.Fatal(out, err)
	}
}
