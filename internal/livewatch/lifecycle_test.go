package livewatch

import (
	"context"
	"errors"
	"fmt"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type lifecycleFixture struct {
	fakeClient
	events []map[string]any
}

func (f *lifecycleFixture) ReportWorkspaceLifecycle(_ context.Context, _ string, payload any) error {
	f.events = append(f.events, payload.(map[string]any))
	return nil
}
func TestLifecycleUsesSameIdentityAndMonotonicOffsets(t *testing.T) {
	f := &lifecycleFixture{}
	cfg := Config{ID: "watch", RequireHypotheses: true}
	capture := sourcesnapshot.Capture{TargetTreeHash: "hash"}
	l := newReviewLifecycle(f, cfg, "/repo", capture, 3, time.Now().Add(-time.Second))
	l.event("capture", "completed")
	l.event("agent_assessment", "running")
	if len(f.events) != 2 || f.events[0]["review_id"] != reviewIdentity(cfg, "/repo", 3) {
		t.Fatal("identity differs from submitted review")
	}
	a := f.events[0]["event"].(map[string]any)["offset_ms"].(int64)
	b := f.events[1]["event"].(map[string]any)["offset_ms"].(int64)
	if a < 1000 || b < a {
		t.Fatal("offsets lost elapsed time")
	}
}

type expiredReviewLifecycle struct {
	lifecycleFixture
	expireAtSubmission bool
}

func (f *expiredReviewLifecycle) StartWorkspace(_ context.Context, _ string, in adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	if f.expireAtSubmission {
		return adversarylabs.WorkspaceReview{}, adversarylabs.ErrAgentAssessmentExpired
	}
	return adversarylabs.WorkspaceReview{ID: in.ReviewID, Status: "queued"}, nil
}

func (f *expiredReviewLifecycle) GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error) {
	return adversarylabs.WorkspaceReview{}, adversarylabs.ErrAgentAssessmentExpired
}

func TestReviewClassifiesExpiryAndReportsTerminalLifecycleStages(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	baseline, err := initialEditFiles(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := capture(t.Context(), repo, baseline)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"submission", "result_delivery"} {
		t.Run(stage, func(t *testing.T) {
			client := &expiredReviewLifecycle{expireAtSubmission: stage == "submission"}
			cfg := Config{ID: "watch", RequireHypotheses: true, editBaseline: baseline}
			cfg.hypotheses = func(context.Context, string, string, int) ([]adversarylabs.WorkspaceHypothesis, error) {
				return []adversarylabs.WorkspaceHypothesis{}, nil
			}
			cfg.lifecycle = newReviewLifecycle(client, cfg, repo, snapshot, 1, time.Now())
			got := review(t.Context(), client, cfg, repo, snapshot, 1)
			if got.Status != "unavailable" || got.Error != adversarylabs.ErrAgentAssessmentExpired.Error() {
				t.Fatalf("unexpected expiry outcome: %+v", got)
			}
			if stage == "submission" && (!got.assessmentExpired || got.ID != "") {
				t.Fatalf("submission expiry did not request a new assessment: %+v", got)
			}
			if stage == "result_delivery" && (got.assessmentExpired || got.ID != reviewIdentity(cfg, repo, 1)) {
				t.Fatalf("delivery expiry did not preserve the accepted review identity: %+v", got)
			}
			failures := map[string]bool{}
			for _, entry := range client.events {
				event := entry["event"].(map[string]any)
				if event["state"] == "failed" && event["error_code"] == "agent_response_timeout" {
					failures[event["stage"].(string)] = true
				}
			}
			if !failures[stage] || !failures["result_delivery"] {
				t.Fatalf("missing terminal lifecycle events: %v", failures)
			}
		})
	}
}

func TestMetadataOnlyChangeDoesNotRegisterOrUploadReview(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("existing branch edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	baseline, err := initialEditFiles(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(time.Second)
	if err := os.Chtimes(filepath.Join(repo, "file.txt"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	client := &lifecycleFixture{}
	w := &worker{cfg: Config{RequireHypotheses: true}, client: client, allowed: func(context.Context, string) bool { return true }, repos: map[string]*repository{repo: {baseline: baseline, pending: true}}, slots: make(chan struct{}, 1), completed: make(chan completion, 1)}
	w.schedule(context.Background())
	w.jobs.Wait()
	result := <-w.completed
	if result.result.Status != "unchanged" {
		t.Fatalf("unexpected result: %+v", result.result)
	}
	if len(client.events) != 0 || client.uploads != 0 || len(client.inputs) != 0 {
		t.Fatalf("no-op produced remote activity: %+v", client)
	}
}

func TestLifecycleReportsHandoffFailureDetails(t *testing.T) {
	for _, tc := range []struct {
		status string
		err    error
		code   string
	}{
		{"superseded", fmt.Errorf("new snapshot"), "superseded"},
		{"incomplete", fmt.Errorf("cannot inspect"), "incomplete"},
		{"", errAgentResponseTimeout, "agent_response_timeout"},
		{"", context.DeadlineExceeded, "deadline_exceeded"},
		{"", context.Canceled, "cancelled"},
	} {
		f := &lifecycleFixture{}
		l := newReviewLifecycle(f, Config{RequireHypotheses: true}, "/repo", sourcesnapshot.Capture{}, 1, time.Now())
		l.failure("agent_assessment", tc.err, &HypothesisResponse{Status: tc.status, RequestID: "request"})
		event := f.events[0]["event"].(map[string]any)
		if event["error_code"] != tc.code || event["reason"] != tc.err.Error() || event["request_id"] != "request" {
			t.Fatalf("wrong failure: %+v", event)
		}
	}
	if len(boundedFailureReason(strings.Repeat("x", 2000))) != 1024 {
		t.Fatal("unbounded reason")
	}
}

func TestFailureWithoutLifecycleIsSafe(t *testing.T) {
	var lifecycle *reviewLifecycle
	lifecycle.failure("capture", fmt.Errorf("capture failed"), nil)
}

func TestTriageFailureCodesAndCancellationPrecedence(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&adversarylabs.ResponseError{Code: "invalid_triage_manifest"}, "invalid_triage_manifest"},
		{&adversarylabs.ResponseError{Code: "triage_diff_too_large"}, "triage_diff_too_large"},
		{errors.Join(&adversarylabs.ResponseError{Code: "triage_timeout"}, context.Canceled), "cancelled"},
		{errors.Join(&adversarylabs.ResponseError{Code: "triage_timeout"}, context.DeadlineExceeded), "deadline_exceeded"},
	} {
		f := &lifecycleFixture{}
		l := newReviewLifecycle(f, Config{RequireHypotheses: true}, "/repo", sourcesnapshot.Capture{}, 1, time.Now())
		l.failure("risk_selection", tc.err, nil)
		if got := f.events[0]["event"].(map[string]any)["error_code"]; got != tc.code {
			t.Fatalf("got %v want %s", got, tc.code)
		}
	}
}

func TestQuickReviewSubmitsAssessmentWithCapturedDiffHash(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	baseline, err := initialEditFiles(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := capture(t.Context(), repo, baseline)
	if err != nil {
		t.Fatal(err)
	}
	var assessedDiff string
	client := &fakeClient{}
	cfg := Config{ID: "watch", RequireHypotheses: true, editBaseline: baseline, assessment: &HypothesisResponse{triageReference: true}}
	cfg.hypotheses = func(_ context.Context, _ string, diff string, _ int) ([]adversarylabs.WorkspaceHypothesis, error) {
		assessedDiff = diff
		return []adversarylabs.WorkspaceHypothesis{}, nil
	}
	result := review(t.Context(), client, cfg, repo, snapshot, 1)
	if result.Status != "ready" || len(client.inputs) != 1 || assessedDiff == "" {
		t.Fatalf("unexpected review outcome: %+v", result)
	}
	input := client.inputs[0]
	if input.AgentDiff != "" || input.Diff == "" || input.ReviewScope != "latest_edit" ||
		input.AgentHandoff["diff_hash"] != Key(assessedDiff) || input.AgentHandoff["snapshot_id"] != snapshot.TargetTreeHash ||
		input.ReviewID != reviewIdentity(cfg, repo, 1) {
		t.Fatalf("the submission did not refer to the assessed patch: %+v", input)
	}
}
