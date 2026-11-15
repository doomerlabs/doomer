package livewatch

import (
	"context"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
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
