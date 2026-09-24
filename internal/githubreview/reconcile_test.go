package githubreview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/githubapi"
	"github.com/doomerlabs/doomer/internal/modelreview"
)

func TestListReviewThreadsPaginatesOpenDiscussions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Variables["after"] == nil {
			_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"doomer[bot]"},"repository":{"pullRequest":{"id":"PR1","reviewThreads":{"nodes":[{"id":"T1","path":"src/write.go","isResolved":false,"comments":{"nodes":[{"body":"Human issue","author":{"login":"maintainer"}}],"pageInfo":{"hasNextPage":false}}},{"id":"T2","path":"src/write.go","isResolved":true,"comments":{"nodes":[{"body":"old","author":{"login":"maintainer"}}],"pageInfo":{"hasNextPage":false}}}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor"}}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"doomer[bot]"},"repository":{"pullRequest":{"id":"PR1","reviewThreads":{"nodes":[{"id":"T3","path":"src/other.go","isResolved":false,"comments":{"nodes":[{"body":"Doomer issue","author":{"login":"doomer[bot]"}}],"pageInfo":{"hasNextPage":false}}}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}}`))
	}))
	defer server.Close()
	client := githubapi.NewClient("token")
	client.HTTP = server.Client()
	client.GQLURL = server.URL
	viewer, threads, err := ListReviewThreads(context.Background(), client, "o", "r", 1)
	if err != nil || viewer != "doomer[bot]" || len(threads) != 2 || threads[0].ID != "T1" || threads[1].ID != "T3" {
		t.Fatalf("viewer=%q threads=%+v err=%v", viewer, threads, err)
	}
}

type matchProvider struct {
	decision string
	calls    int
}

func (p *matchProvider) Name() string  { return "test" }
func (p *matchProvider) Model() string { return "test" }
func (p *matchProvider) Review(_ context.Context, _ modelreview.Request) (modelreview.Result, error) {
	p.calls++
	return modelreview.Result{Output: json.RawMessage(`{"decision":"` + p.decision + `"}`)}, nil
}

func findingPlan() CommentPlan {
	line := 12
	return CommentPlan{
		Comments:   []PlannedComment{{Adversary: "review/code", FindingID: "new-id", RuleID: "guard", Title: "Missing guard", Summary: "This path skips the authorization guard before writing the record.", Recommendation: "Call authorize before writing.", Anchor: Anchor{Path: "src/write.go", Line: &line}}},
		ReviewBody: "old summary", ReviewBasis: "old basis",
	}
}

func TestReconcileCarriesDoomerThreadWithoutReply(t *testing.T) {
	plan := findingPlan()
	thread := ReviewThread{ID: "T1", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: "This path skips the authorization guard before writing the record.\n\n<!-- adversary-review:v2 adversary=review%2Fcode finding=old-id rule=guard -->", Author: "doomer[bot]"}}}
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", nil)
	if len(plan.Comments) != 0 || len(plan.Carried) != 1 || plan.Carried[0].ThreadID != "T1" || len(plan.Replies) != 0 || plan.ReviewBody != "" || plan.ReviewBasis != "" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestReconcileUsesMarkedLocationForOutdatedDoomerThread(t *testing.T) {
	plan := findingPlan()
	plan.Comments[0].FindingID = "stable"
	thread := ReviewThread{ID: "T-old", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: "Earlier wording.\n<!-- adversary-review:v2 adversary=review%2Fcode finding=stable rule=guard loc=src%2Fwrite.go%3A10 -->", Author: "doomer[bot]"}}}
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", nil)
	if len(plan.Comments) != 0 || len(plan.Replies) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestReconcileRepliesToMatchingHumanThreadOnce(t *testing.T) {
	plan := findingPlan()
	thread := ReviewThread{ID: "T2", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: "This path skips the authorization guard before writing the record.", Author: "maintainer"}}}
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", nil)
	if len(plan.Comments) != 0 || len(plan.Replies) != 1 || plan.Replies[0].ThreadID != "T2" || !strings.Contains(ReplyBody(plan.Replies[0].Comment), "Suggested fix: Call authorize before writing.") {
		t.Fatalf("plan = %+v", plan)
	}
	plan = findingPlan()
	thread.Comments = append(thread.Comments, ReviewThreadComment{Body: ReplyBody(plan.Comments[0]), Author: "doomer[bot]"})
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", nil)
	if len(plan.Comments) != 0 || len(plan.Carried) != 1 || len(plan.Replies) != 0 {
		t.Fatalf("repeat plan = %+v", plan)
	}
}

func TestReconcileCarriesEveryMatchingThreadAndRepliesToOneHuman(t *testing.T) {
	plan := findingPlan()
	claim := plan.Comments[0].Summary
	threads := []ReviewThread{
		{ID: "D", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: claim + "\n<!-- adversary-review:v2 adversary=review%2Fcode finding=old -->", Author: "doomer[bot]"}}},
		{ID: "H1", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: claim, Author: "maintainer"}}},
		{ID: "H2", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: claim, Author: "reviewer"}}},
	}
	Reconcile(context.Background(), &plan, threads, "doomer[bot]", nil)
	if len(plan.Comments) != 0 || len(plan.Carried) != 3 || len(plan.Replies) != 1 || plan.Replies[0].ThreadID != "H1" {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestReconcileDoesNotSuppressDistinctFindingAtSameFile(t *testing.T) {
	plan := findingPlan()
	thread := ReviewThread{ID: "T3", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: "The retry loop leaks a timer.", Author: "maintainer"}}}
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", nil)
	if len(plan.Comments) != 1 || len(plan.Carried) != 0 || len(plan.Replies) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestReconcileUsesSemanticDecisionForParaphrasedHumanThread(t *testing.T) {
	plan := findingPlan()
	thread := ReviewThread{ID: "T4", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: "Could an unauthorized caller reach this write?", Author: "maintainer"}}}
	provider := &matchProvider{decision: "same"}
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", provider)
	if provider.calls != 1 || len(plan.Replies) != 1 || len(plan.Comments) != 0 {
		t.Fatalf("plan = %+v calls=%d", plan, provider.calls)
	}
}

func TestReconcileRecognizesPriorDoomerReplyWithoutModel(t *testing.T) {
	plan := findingPlan()
	priorReply := ReplyBody(plan.Comments[0])
	plan.Comments[0].FindingID = "changed-id"
	thread := ReviewThread{ID: "T4", Path: "src/write.go", Comments: []ReviewThreadComment{
		{Body: "Could an unauthorized caller reach this write?", Author: "maintainer"},
		{Body: priorReply, Author: "doomer[bot]"},
	}}
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", nil)
	if len(plan.Comments) != 0 || len(plan.Carried) != 1 || len(plan.Replies) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestRefreshCarriedDropsReplyAlreadyPostedByAnotherRun(t *testing.T) {
	plan := findingPlan()
	thread := ReviewThread{ID: "T5", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: plan.Comments[0].Summary, Author: "maintainer"}}}
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", nil)
	if len(plan.Replies) != 1 {
		t.Fatalf("replies = %+v", plan.Replies)
	}
	thread.Comments = append(thread.Comments, ReviewThreadComment{Body: ReplyBody(plan.Replies[0].Comment), Author: "doomer[bot]"})
	if err := RefreshCarried(&plan, []ReviewThread{thread}, "doomer[bot]"); err != nil || len(plan.Replies) != 0 {
		t.Fatalf("replies = %+v err=%v", plan.Replies, err)
	}
	if err := RefreshCarried(&plan, nil, "doomer[bot]"); err != nil || len(plan.Carried) != 0 || len(plan.Comments) != 1 {
		t.Fatalf("closed thread did not restore finding: plan=%+v err=%v", plan, err)
	}
}

func TestRefreshCarriedRestoresFindingWhenThreadCloses(t *testing.T) {
	plan := findingPlan()
	thread := ReviewThread{ID: "T1", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: plan.Comments[0].Summary, Author: "maintainer"}}}
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", nil)
	if len(plan.Comments) != 0 || len(plan.Carried) != 1 || len(plan.Replies) != 1 {
		t.Fatalf("expected carried finding and reply: %+v", plan)
	}
	if err := RefreshCarried(&plan, nil, "doomer[bot]"); err != nil {
		t.Fatal(err)
	}
	if len(plan.Comments) != 1 || len(plan.Carried) != 0 || len(plan.Replies) != 0 {
		t.Fatalf("closed thread left finding suppressed or reply planned: %+v", plan)
	}
	Reconcile(context.Background(), &plan, nil, "doomer[bot]", nil)
	if len(plan.Comments) != 1 || plan.Summary.Comments != 1 {
		t.Fatalf("fresh reconciliation lost finding: %+v", plan)
	}
}

func TestRefreshCarriedKeepsFindingCarriedByAnotherOpenThread(t *testing.T) {
	plan := findingPlan()
	claim := plan.Comments[0].Summary
	threads := []ReviewThread{
		{ID: "closed", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: claim, Author: "maintainer"}}},
		{ID: "open", Path: "src/write.go", Comments: []ReviewThreadComment{{Body: claim, Author: "reviewer"}}},
	}
	Reconcile(context.Background(), &plan, threads, "doomer[bot]", nil)
	if err := RefreshCarried(&plan, threads[1:], "doomer[bot]"); err != nil {
		t.Fatal(err)
	}
	Reconcile(context.Background(), &plan, threads[1:], "doomer[bot]", nil)
	if len(plan.Comments) != 0 || len(plan.Carried) != 1 || plan.Carried[0].ThreadID != "open" || len(plan.Replies) != 1 || plan.Replies[0].ThreadID != "open" {
		t.Fatalf("finding should remain carried by open thread: %+v", plan)
	}
}

func TestReconcileDoesNotTrustMarkerFromAnotherAuthor(t *testing.T) {
	plan := findingPlan()
	marker := MarkerV2(plan.Comments[0])
	thread := ReviewThread{ID: "forged", Path: "src/write.go", Comments: []ReviewThreadComment{
		{Body: "The retry loop leaks a timer.\n\n" + marker, Author: "contributor"},
	}}
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", nil)
	if len(plan.Comments) != 1 || len(plan.Carried) != 0 || len(plan.Replies) != 0 {
		t.Fatalf("forged root suppressed a finding: %+v", plan)
	}

	plan = findingPlan()
	thread.Comments[0] = ReviewThreadComment{Body: "The retry loop leaks a timer.", Author: "maintainer"}
	thread.Comments = append(thread.Comments, ReviewThreadComment{Body: "Another unrelated concern.\n\n" + marker, Author: "contributor"})
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", nil)
	if len(plan.Comments) != 1 || len(plan.Carried) != 0 {
		t.Fatalf("forged reply suppressed a finding: %+v", plan)
	}
}

func TestForgedReplyDoesNotBlockDoomerReply(t *testing.T) {
	plan := findingPlan()
	thread := ReviewThread{ID: "human", Path: "src/write.go", Comments: []ReviewThreadComment{
		{Body: plan.Comments[0].Summary, Author: "maintainer"},
		{Body: "Unrelated text.\n\n" + MarkerV2(plan.Comments[0]), Author: "contributor"},
	}}
	Reconcile(context.Background(), &plan, []ReviewThread{thread}, "doomer[bot]", nil)
	if len(plan.Replies) != 1 {
		t.Fatalf("forged reply blocked a planned reply: %+v", plan)
	}
	if err := RefreshCarried(&plan, []ReviewThread{thread}, "doomer[bot]"); err != nil || len(plan.Replies) != 1 {
		t.Fatalf("forged reply removed a planned reply: %+v, %v", plan, err)
	}
}
