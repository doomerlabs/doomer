package livewatch

import (
	"context"
	"errors"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"testing"
	"time"
)

func TestUnassessedSnapshotRequiresReason(t *testing.T) {
	for _, status := range []string{"superseded", "incomplete"} {
		pending := &hypothesisWait{reply: make(chan HypothesisResponse, 1)}
		w := &worker{hypotheses: map[string]*hypothesisWait{"request": pending}}
		response := HypothesisResponse{RequestID: "request", Status: status, Hypotheses: []adversarylabs.WorkspaceHypothesis{}}
		if w.recordHypotheses(response) == nil {
			t.Fatal("missing reason accepted")
		}
		response.Reason = "Newer source replaced this captured snapshot"
		if err := w.recordHypotheses(response); err != nil {
			t.Fatal(err)
		}
		got := <-pending.reply
		if got.Status != status || got.Reason != response.Reason {
			t.Fatalf("lost outcome: %+v", got)
		}
		if w.recordHypotheses(response) == nil {
			t.Fatal("accepted repeated single-use request")
		}
	}
}

func TestEverySelectedAreaRequiresEvidence(t *testing.T) {
	pending := &hypothesisWait{request: HypothesisRequest{Areas: []adversarylabs.WorkspaceReviewArea{{ID: "auth"}}}, reply: make(chan HypothesisResponse, 1)}
	w := &worker{hypotheses: map[string]*hypothesisWait{"request": pending}}
	response := HypothesisResponse{RequestID: "request", Status: "assessed", Hypotheses: []adversarylabs.WorkspaceHypothesis{}}
	if w.recordHypotheses(response) == nil {
		t.Fatal("accepted empty blanket assessment")
	}
	response.Assessments = []AreaAssessment{{AreaID: "auth", Status: "no_issue", Evidence: "Checked the caller: authorization is enforced before this function."}}
	if err := w.recordHypotheses(response); err != nil {
		t.Fatal(err)
	}
}

func TestNoSelectedAreasRejectsInventedAssessment(t *testing.T) {
	pending := &hypothesisWait{reply: make(chan HypothesisResponse, 1)}
	w := &worker{hypotheses: map[string]*hypothesisWait{"request": pending}}
	response := HypothesisResponse{RequestID: "request", Status: "assessed", Hypotheses: []adversarylabs.WorkspaceHypothesis{},
		Assessments: []AreaAssessment{{AreaID: "invented", Status: "no_issue", Evidence: "Checked the edit."}}}
	if w.recordHypotheses(response) == nil {
		t.Fatal("accepted an assessment when triage selected no areas")
	}
	response.Assessments = []AreaAssessment{}
	if err := w.recordHypotheses(response); err != nil {
		t.Fatal(err)
	}
}

func TestAgentResponseTimeoutStartsAfterRetrieval(t *testing.T) {
	p := &hypothesisWait{delivered: make(chan struct{}), expired: make(chan struct{}), reply: make(chan HypothesisResponse, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.wait(ctx, 20*time.Millisecond); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("unseen request expired: %v", err)
	case <-time.After(60 * time.Millisecond):
	}
	close(p.delivered)
	select {
	case err := <-done:
		if !errors.Is(err, errAgentResponseTimeout) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("retrieved request never expired")
	}
	p.request = HypothesisRequest{ID: "late", Repository: "/repo", Revision: 1}
	w := &worker{
		hypotheses: map[string]*hypothesisWait{"late": p},
		repos:      map[string]*repository{"/repo": {running: true, version: 1}},
	}
	if err := w.recordHypotheses(HypothesisResponse{RequestID: "late", Hypotheses: []adversarylabs.WorkspaceHypothesis{}}); !errors.Is(err, errAgentResponseTimeout) {
		t.Fatalf("accepted late response: %v", err)
	}
	if report := w.response(false); len(report.HypothesisRequests) != 0 || !report.OtherReviewPending {
		t.Fatalf("expired request should be hidden while its review finishes: %+v", report)
	}
}

func TestUndeliveredAgentRequestHonorsCancellation(t *testing.T) {
	p := &hypothesisWait{delivered: make(chan struct{}), expired: make(chan struct{}), reply: make(chan HypothesisResponse, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.wait(ctx, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAgentResponseDeadlineStartsOnceOnRetrieval(t *testing.T) {
	p := &hypothesisWait{delivered: make(chan struct{}), expired: make(chan struct{}), reply: make(chan HypothesisResponse, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := p.wait(ctx, time.Second); done <- err }()
	w := &worker{hypotheses: map[string]*hypothesisWait{"request": p}}
	_ = w.response(false)
	select {
	case <-p.delivered:
		t.Fatal("status response marked request retrieved")
	default:
	}
	w.markHypothesesDelivered()
	w.markHypothesesDelivered()
	p.reply <- HypothesisResponse{Status: "assessed"}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStopCanHandOffNewAssessmentAfterEarlierRequests(t *testing.T) {
	w := &worker{hypotheses: map[string]*hypothesisWait{"first": {}}}
	if w.repeatStopHandoff() || !w.repeatStopHandoff() {
		t.Fatal("the first request should get one Stop handoff")
	}
	w.hypotheses["replacement"] = &hypothesisWait{delivered: make(chan struct{})}
	if w.repeatStopHandoff() || !w.repeatStopHandoff() {
		t.Fatal("a replacement request should get its own Stop handoff")
	}
}

func TestCodexStopOnlyOffersUnreadAssessments(t *testing.T) {
	w := &worker{cfg: Config{Agent: "codex"}, hypotheses: map[string]*hypothesisWait{"fetched": {delivered: make(chan struct{})}}}
	w.markHypothesesDelivered()
	if !w.repeatStopHandoff() {
		t.Fatal("a fetched assessment should not reopen the Codex turn")
	}
	w.hypotheses["replacement"] = &hypothesisWait{delivered: make(chan struct{})}
	if w.repeatStopHandoff() || !w.repeatStopHandoff() {
		t.Fatal("an unread replacement should get exactly one Stop handoff")
	}
}

func TestAssessmentDenialIsAnExplicitInternalOutcome(t *testing.T) {
	pending := &hypothesisWait{request: HypothesisRequest{Repository: "/repo"}, reply: make(chan HypothesisResponse, 1)}
	w := &worker{
		cfg:        Config{Agent: "claude"},
		allowed:    func(context.Context, string) bool { return true },
		hypotheses: map[string]*hypothesisWait{"request": pending},
	}
	if err := w.recordHypotheses(HypothesisResponse{RequestID: "request", Status: assessmentStatusDenied}); err == nil {
		t.Fatal("accepted a denial through the assessment submission endpoint")
	}
	w.recordAssessmentDenial(context.Background(), "request", AssessmentOversightDenied)
	select {
	case result := <-pending.reply:
		if result.Status != assessmentStatusDenied || result.denial != AssessmentOversightDenied {
			t.Fatalf("lost the denial outcome: %+v", result)
		}
	default:
		t.Fatal("did not release the waiter after denial")
	}
}

func TestOnlyMatchingAssessmentWaitsAreExcludedFromOtherReviewWork(t *testing.T) {
	w := &worker{
		repos:      map[string]*repository{"/awaiting": {running: true, version: 3}},
		hypotheses: map[string]*hypothesisWait{"request": {request: HypothesisRequest{Repository: "/awaiting", Revision: 3}}},
	}
	if response := w.response(false); !response.ReviewPending || response.OtherReviewPending {
		t.Fatalf("assessment was counted as other work: %+v", response)
	}
	w.repos["/running"] = &repository{running: true}
	if !w.response(false).OtherReviewPending {
		t.Fatal("missed a different repository's running review")
	}
	delete(w.repos, "/running")
	w.repos["/awaiting"].pending = true
	if !w.response(false).OtherReviewPending {
		t.Fatal("missed a queued change in the same repository")
	}
	w.repos["/awaiting"].pending = false
	w.repos["/awaiting"].version++
	if !w.response(false).OtherReviewPending {
		t.Fatal("mistook an earlier assessment wait for the current revision")
	}
}
