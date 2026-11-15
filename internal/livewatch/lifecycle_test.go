package livewatch

import (
	"context"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"os"
	"path/filepath"
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
