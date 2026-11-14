package livewatch

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestAdvisoryKeepsWatchAliveUntilExactRevisionIsAcknowledged(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix IPC")
	}
	repo := makeRepository(t, t.TempDir(), "repo")
	var calls atomic.Int32
	cfg := Config{ID: t.TempDir(), ControlKey: Key(t.TempDir()), Directory: repo, DrainTimeout: time.Second,
		Notices: func() []adversarylabs.ClientNotice {
			return []adversarylabs.ClientNotice{{ID: "maintenance", Revision: 3, NotifyUser: true, Blocking: true}}
		},
		AcknowledgeNotice: func(id string, revision int) error {
			if id != "maintenance" || revision != 3 {
				return fmt.Errorf("wrong notice")
			}
			calls.Add(1)
			return nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
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
	result, err := Call(ctx, cfg.ControlKey, "finish")
	if err != nil || len(result.Notices) != 1 || !result.Notices[0].NotifyUser {
		t.Fatal(result, err)
	}
	if _, live := Available(ctx, cfg.ControlKey); !live {
		t.Fatal("watch exited before advisory delivery")
	}
	if _, err := AcknowledgeNotice(ctx, cfg.ControlKey, "maintenance", 2); err == nil {
		t.Fatal("accepted old revision")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid acknowledgement reached persistence")
	}
	if _, err := Call(ctx, cfg.ControlKey, "checkpoint"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("reading a report acknowledged the notice")
	}
	result, err = AcknowledgeNotice(ctx, cfg.ControlKey, "maintenance", 3)
	if err != nil || result.Notices[0].NotifyUser {
		t.Fatal(result, err)
	}
	if calls.Load() != 1 {
		t.Fatal("missing delivery receipt")
	}
	if _, err = Call(ctx, cfg.ControlKey, "finish"); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("watch did not finish")
	}
}
