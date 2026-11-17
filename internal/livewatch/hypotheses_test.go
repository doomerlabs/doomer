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
	w := &worker{hypotheses: map[string]*hypothesisWait{"late": p}}
	if err := w.recordHypotheses(HypothesisResponse{RequestID: "late", Hypotheses: []adversarylabs.WorkspaceHypothesis{}}); !errors.Is(err, errAgentResponseTimeout) {
		t.Fatalf("accepted late response: %v", err)
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
