package livewatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

type feedbackClient struct {
	fakeClient
	recorded []Feedback
	fail     bool
}

func (f *feedbackClient) StartWorkspace(ctx context.Context, token string, input adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	result, err := f.fakeClient.StartWorkspace(ctx, token, input)
	result.URL = "https://example.test/look-closer/org/repo/" + strings.Repeat("a", 64)
	result.Early.Leads = []adversarylabs.WorkspaceLead{{ID: strings.Repeat("a", 64), Path: "file.txt", Line: 1, Concern: "first"}, {ID: strings.Repeat("b", 64), Path: "file.txt", Line: 1, Concern: "second"}}
	return result, err
}
func (f *feedbackClient) ReportWorkspaceLead(_ context.Context, _ string, review, lead, status, reason string, agrees *bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("SaaS unavailable")
	}
	f.recorded = append(f.recorded, Feedback{ReviewID: review, LeadID: lead, Status: status, Reason: reason, Agrees: agrees})
	return nil
}

func TestWatchRecordsFeedbackBeforeFinalStop(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &feedbackClient{}
	cfg, done := startWorker(t, repo, client, time.Second, func(context.Context, string) bool { return true })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Call(ctx, cfg.ControlKey, "finish")
	if err != nil || len(result.Findings) != 2 {
		t.Fatalf("findings: %+v %v", result, err)
	}
	if _, live := Available(ctx, cfg.ControlKey); !live {
		t.Fatal("watch exited before feedback")
	}
	agrees, disagrees := true, false
	entries := []Feedback{{ReviewID: result.Findings[0].ReviewID, LeadID: strings.Repeat("a", 64), Status: "fixed", Agrees: &agrees, Reason: "Added the missing check."}, {ReviewID: result.Findings[0].ReviewID, LeadID: strings.Repeat("b", 64), Status: "dismissed", Agrees: &disagrees, Reason: "Existing validation covers it."}}
	client.mu.Lock()
	client.fail = true
	client.mu.Unlock()
	failed, err := SubmitFeedback(ctx, cfg.ControlKey, entries)
	if err == nil || failed.Findings[0].Disposition != nil {
		t.Fatal("failed SaaS write counted as recorded")
	}
	client.mu.Lock()
	client.fail = false
	client.mu.Unlock()
	recorded, err := SubmitFeedback(ctx, cfg.ControlKey, entries)
	if err != nil || recorded.Findings[0].Disposition == nil || recorded.Findings[1].Disposition == nil {
		t.Fatalf("feedback: %+v %v", recorded, err)
	}
	client.mu.Lock()
	n := len(client.recorded)
	client.mu.Unlock()
	if n != 2 {
		t.Fatalf("SaaS received %d records", n)
	}
	result, err = Call(ctx, cfg.ControlKey, "finish")
	if err != nil || len(result.Findings) != 2 {
		t.Fatalf("final result: %+v %v", result, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("watch did not exit after recording feedback")
	}
	if _, live := Available(ctx, cfg.ControlKey); live {
		t.Fatal("finished watch is still live")
	}
}

func TestWatchRejectsInvalidFeedbackBeforeSending(t *testing.T) {
	client := &feedbackClient{}
	lead := strings.Repeat("a", 64)
	w := &worker{client: client, allowed: func(context.Context, string) bool { return true }, findings: map[string]Finding{"review/" + lead: {ReviewID: "review", Lead: adversarylabs.WorkspaceLead{ID: lead}}}}
	agrees, disagrees := true, false
	for _, entry := range []Feedback{
		{ReviewID: "other", LeadID: lead, Status: "fixed", Agrees: &agrees, Reason: "reason"},
		{ReviewID: "review", LeadID: lead, Status: "fixed", Agrees: &disagrees, Reason: "reason"},
		{ReviewID: "review", LeadID: lead, Status: "dismissed", Agrees: &agrees, Reason: "reason"},
		{ReviewID: "review", LeadID: lead, Status: "deferred", Reason: " "},
	} {
		if err := w.recordFeedback(context.Background(), []Feedback{entry}); err == nil {
			t.Fatal("accepted invalid response")
		}
	}
	if len(client.recorded) != 0 {
		t.Fatal("sent invalid response to SaaS")
	}
	if err := w.recordFeedback(context.Background(), []Feedback{{ReviewID: "review", LeadID: lead, Status: "deferred", Reason: "Outside this task", Agrees: nil}}); err != nil {
		t.Fatal(err)
	}
	if len(client.recorded) != 1 || client.recorded[0].Status != "deferred" || client.recorded[0].Agrees != nil {
		t.Fatal("deferral agreement was inferred")
	}
}
