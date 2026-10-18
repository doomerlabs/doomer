package cmd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

type reviewTestClock struct {
	timers  int
	stopped int
	tick    bool
	cancel  context.CancelFunc
}
type reviewTestTimer struct {
	clock *reviewTestClock
	ch    chan time.Time
}

func (c *reviewTestClock) Now() time.Time { return time.Time{} }
func (c *reviewTestClock) NewTimer(d time.Duration) application.Timer {
	c.timers++
	ch := make(chan time.Time, 1)
	if c.tick {
		ch <- time.Time{}
	}
	if c.cancel != nil {
		c.cancel()
	}
	return &reviewTestTimer{c, ch}
}
func (t *reviewTestTimer) C() <-chan time.Time { return t.ch }
func (t *reviewTestTimer) Stop() bool          { t.clock.stopped++; return true }

type reviewTestClient struct {
	states []string
	calls  int
	err    error
}

func (c *reviewTestClient) GetReview(ctx context.Context, token, id string) (adversarylabs.ReviewStatus, error) {
	c.calls++
	if token != "token" || id != "job" {
		panic("wrong credentials or review")
	}
	if c.err != nil {
		return adversarylabs.ReviewStatus{}, c.err
	}
	return adversarylabs.ReviewStatus{ReviewSubmission: adversarylabs.ReviewSubmission{ID: id, Status: c.states[c.calls-1]}, Result: &adversarylabs.ReviewResult{Conclusion: "failure", Summary: "Complete result"}}, nil
}

func TestWaitForReviewLifecycle(t *testing.T) {
	accepted := adversarylabs.ReviewSubmission{ID: "job", Status: "queued"}
	for _, terminal := range []string{"completed", "failed", "cancelled", "expired"} {
		t.Run(terminal, func(t *testing.T) {
			clock := &reviewTestClock{tick: true}
			client := &reviewTestClient{states: []string{"queued", "running", terminal}}
			got, err := waitForReview(context.Background(), clock, client, "token", accepted, time.Second)
			if got.Status != terminal || got.Result.Summary != "Complete result" || client.calls != 3 || clock.timers != 2 || clock.stopped != 2 {
				t.Fatalf("result=%+v calls=%d timers=%d", got, client.calls, clock.timers)
			}
			if (err == nil) != (terminal == "completed") {
				t.Fatalf("terminal error: %v", err)
			}
		})
	}
}

func TestWaitForReviewCancellationAndLookupFailure(t *testing.T) {
	accepted := adversarylabs.ReviewSubmission{ID: "job", Status: "queued"}
	t.Run("already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := &reviewTestClient{}
		got, err := waitForReview(ctx, &reviewTestClock{}, client, "token", accepted, time.Second)
		if !errors.Is(err, context.Canceled) || got.ID != "job" || client.calls != 0 {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("during poll interval", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		clock := &reviewTestClock{cancel: cancel}
		client := &reviewTestClient{states: []string{"running"}}
		got, err := waitForReview(ctx, clock, client, "token", accepted, time.Second)
		if !errors.Is(err, context.Canceled) || got.Status != "running" || clock.stopped != 1 {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		_, err := waitForReview(ctx, &reviewTestClock{}, &reviewTestClient{}, "token", accepted, time.Second)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
	})
	t.Run("lookup error", func(t *testing.T) {
		failure := errors.New("API unavailable")
		got, err := waitForReview(context.Background(), &reviewTestClock{}, &reviewTestClient{err: failure}, "token", accepted, time.Second)
		if !errors.Is(err, failure) || got.ID != "job" || got.Status != "queued" {
			t.Fatalf("%+v %v", got, err)
		}
	})
}

func TestReviewWaitFlagValidation(t *testing.T) {
	isolate(t)
	for _, args := range [][]string{{"--timeout", "1s"}, {"--poll-interval", "1s"}, {"--wait", "--timeout", "0s"}, {"--wait", "--poll-interval", "-1s"}} {
		if _, err := execute(t, "", append([]string{"review"}, args...)...); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
