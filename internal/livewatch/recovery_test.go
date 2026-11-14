package livewatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestDeniedAndFailedAdvisoryReceiptRemainPendingAcrossReads(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	var fail atomic.Bool
	fail.Store(true)
	var calls atomic.Int32
	var revision atomic.Int32
	revision.Store(3)
	cfg := Config{ID: t.TempDir(), ControlKey: Key(t.TempDir()), Directory: repo, DrainTimeout: time.Second,
		Notices: func() []adversarylabs.ClientNotice {
			return []adversarylabs.ClientNotice{{ID: "maintenance", Revision: int(revision.Load()), NotifyUser: true}}
		},
		AcknowledgeNotice: func(string, int) error {
			calls.Add(1)
			if fail.Load() {
				return errors.New("receipt storage unavailable")
			}
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, &fakeClient{}, func(context.Context, string) bool { return true }) }()
	for {
		if _, live := Available(ctx, cfg.ControlKey); live {
			break
		}
		select {
		case err := <-done:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// A host permission denial prevents the acknowledgement command from
	// running at all. Reading/finishing must leave it pending without a write.
	for _, op := range []string{"results", "finish"} {
		out, err := Call(ctx, cfg.ControlKey, op)
		if err != nil || len(out.Notices) != 1 || !out.Notices[0].NotifyUser || calls.Load() != 0 {
			t.Fatalf("denied acknowledgement was invented by %s: %+v %v", op, out, err)
		}
	}
	failed, err := AcknowledgeNotice(ctx, cfg.ControlKey, "maintenance", 3)
	if err == nil || !failed.Notices[0].NotifyUser {
		t.Fatal("failed receipt marked delivered", failed, err)
	}
	for _, op := range []string{"results", "checkpoint", "finish"} {
		out, err := Call(ctx, cfg.ControlKey, op)
		if err != nil || len(out.Notices) != 1 || !out.Notices[0].NotifyUser {
			t.Fatalf("%s lost pending receipt: %+v %v", op, out, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("reading or finishing retried the failed write", calls.Load())
	}
	fail.Store(false)
	out, err := AcknowledgeNotice(ctx, cfg.ControlKey, "maintenance", 3)
	if err != nil || out.Notices[0].NotifyUser {
		t.Fatal(out, err)
	}
	revision.Store(4)
	out, err = Call(ctx, cfg.ControlKey, "results")
	if err != nil || !out.Notices[0].NotifyUser {
		t.Fatal("old receipt hid a new revision", out, err)
	}
	if _, err = AcknowledgeNotice(ctx, cfg.ControlKey, "maintenance", 3); err == nil {
		t.Fatal("stale revision accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("stale revision reached persistence")
	}
	if _, err = AcknowledgeNotice(ctx, cfg.ControlKey, "maintenance", 4); err != nil {
		t.Fatal(err)
	}
	if _, err = Call(ctx, cfg.ControlKey, "finish"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("acknowledged worker did not exit")
	}
}

type partialFeedbackClient struct {
	feedbackClient
	failLead string
}

func (f *partialFeedbackClient) ReportWorkspaceLead(ctx context.Context, token, review, lead, status, reason string, agrees *bool) error {
	if lead == f.failLead {
		return errors.New("feedback service unavailable")
	}
	return f.feedbackClient.ReportWorkspaceLead(ctx, token, review, lead, status, reason, agrees)
}

func TestFeedbackRecoveryPreservesPartialSuccessAndPermissionDenial(t *testing.T) {
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	client := &partialFeedbackClient{failLead: second}
	allowed := true
	w := &worker{client: client, allowed: func(context.Context, string) bool { return allowed }, findings: map[string]Finding{
		"review/" + first:  {ReviewID: "review", Lead: adversarylabs.WorkspaceLead{ID: first}},
		"review/" + second: {ReviewID: "review", Lead: adversarylabs.WorkspaceLead{ID: second}},
	}}
	entries := []Feedback{{ReviewID: "review", LeadID: first, Status: "deferred", Reason: "Requires user decision"}, {ReviewID: "review", LeadID: second, Status: "deferred", Reason: "Requires user decision"}}
	if err := w.recordFeedback(context.Background(), entries); err == nil {
		t.Fatal("service failure was hidden")
	}
	if w.findings["review/"+first].Disposition == nil || w.findings["review/"+second].Disposition != nil || len(client.recorded) != 1 {
		t.Fatal("partial success was lost or failed write was recorded")
	}
	allowed = false
	client.failLead = ""
	if err := w.recordFeedback(context.Background(), entries[1:]); err == nil {
		t.Fatal("revoked permission was ignored")
	}
	if len(client.recorded) != 1 || w.findings["review/"+second].Disposition != nil {
		t.Fatal("denied response changed state")
	}
	allowed = true
	if err := w.recordFeedback(context.Background(), entries[1:]); err != nil {
		t.Fatal(err)
	}
	if len(client.recorded) != 2 || w.findings["review/"+second].Disposition == nil {
		t.Fatal("explicit retry did not finish the pending response")
	}
}

func TestFixFollowedByNewReviewRequiresNewDecisions(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	file := filepath.Join(repo, "file.txt")

	cfg, done := startWorker(t, repo, &feedbackClient{}, time.Second, func(context.Context, string) bool { return true })
	if err := os.WriteFile(file, []byte("first change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := Call(ctx, cfg.ControlKey, "finish")
	if err != nil || len(first.Findings) != 2 {
		t.Fatal(first, err)
	}
	agrees := true
	entries := make([]Feedback, 0, 2)
	for _, f := range first.Findings {
		entries = append(entries, Feedback{ReviewID: f.ReviewID, LeadID: f.Lead.ID, Status: "fixed", Reason: "Updated the implementation", Agrees: &agrees})
	}
	if _, err = SubmitFeedback(ctx, cfg.ControlKey, entries); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, []byte("second change after fix\n"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := Call(ctx, cfg.ControlKey, "finish")
	if err != nil || len(next.Findings) != 4 {
		t.Fatalf("new review missing: %+v %v", next, err)
	}
	entries = nil
	for _, f := range next.Findings {
		if f.ReviewID == first.Findings[0].ReviewID {
			if f.Disposition == nil {
				t.Fatal("lost prior decision")
			}
			continue
		}
		if f.Disposition != nil {
			t.Fatal("prior fix silently answered new review")
		}
		entries = append(entries, Feedback{ReviewID: f.ReviewID, LeadID: f.Lead.ID, Status: "deferred", Reason: "New concern needs follow-up"})
	}
	if len(entries) != 2 {
		t.Fatal("expected two new decisions")
	}
	if _, live := Available(ctx, cfg.ControlKey); !live {
		t.Fatal("worker exited with new unanswered findings")
	}
	if _, err = SubmitFeedback(ctx, cfg.ControlKey, entries); err != nil {
		t.Fatal(err)
	}
	if _, err = Call(ctx, cfg.ControlKey, "finish"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("worker did not exit")
	}
}

func TestInterruptCancelsInflightReviewWithoutReportingSuccess(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")

	client := &fakeClient{block: true, started: make(chan adversarylabs.WorkspaceSubmission, 1)}
	cfg, done := startWorker(t, repo, client, time.Second, func(context.Context, string) bool { return true })
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-client.started:
	case <-ctx.Done():
		t.Fatal("review never started")
	}
	out, err := Call(ctx, cfg.ControlKey, "cancel")
	if err != nil || len(out.Results) != 1 || out.Results[0].Status != "unavailable" || out.Results[0].Error == "" {
		t.Fatal("interrupted review appeared successful", out, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("interrupted worker or request remained running")
	}
	if _, live := Available(ctx, cfg.ControlKey); live {
		t.Fatal("interrupted worker still accepts commands")
	}
}
