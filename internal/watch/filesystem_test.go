package watch

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCancelledWatcherStartupDoesNotWarnAboutFilesystemNotifications(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	runner := Runner{Root: t.TempDir(), Output: &output}
	if err := runner.Run(ctx, t.TempDir(), false); err != nil {
		t.Fatalf("cancelled watcher returned an error: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("cancelled watcher emitted a warning: %s", output.String())
	}
}

func TestFilesystemChangesAndHookRequestsWakeWatcher(t *testing.T) {
	for _, change := range []string{"existing-file", "new-directory", "hook-request"} {
		t.Run(change, func(t *testing.T) {
			dir, root := repository(t), t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events, err := newFilesystemEvents(ctx, Runner{Root: root, Output: io.Discard}, dir)
			if err != nil {
				t.Fatal(err)
			}
			defer events.watcher.Close()
			switch change {
			case "existing-file":
				err = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600)
			case "new-directory":
				err = os.Mkdir(filepath.Join(dir, "nested"), 0700)
				if err == nil {
					err = os.WriteFile(filepath.Join(dir, "nested", "new.go"), []byte("package nested\n"), 0600)
				}
			case "hook-request":
				err = Write(StatePath(root, dir), State{RequestedAt: time.Now()})
			}
			if err != nil {
				t.Fatal(err)
			}
			waitCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			defer stop()
			if !events.wait(waitCtx, time.Minute, 0) {
				t.Fatal("filesystem event did not wake capture")
			}
		})
	}
}

func TestFilesystemIgnoresBuildOutputAndDoesNotPollAtIdle(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "build"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := newFilesystemEvents(ctx, Runner{Root: root, Output: io.Discard}, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer events.watcher.Close()
	if err = os.WriteFile(filepath.Join(dir, "build", "output"), []byte("generated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Write(filepath.Join(root, "heartbeat.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	if events.wait(waitCtx, time.Minute, 0) {
		t.Fatal("ignored output or heartbeat woke capture")
	}
}

func TestFilesystemFallbackAndRetryDeadlines(t *testing.T) {
	for _, retry := range []time.Duration{0, 5 * time.Millisecond} {
		var events *filesystemEvents
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if !events.wait(ctx, 10*time.Millisecond, retry) {
			t.Fatal("fallback did not wake capture")
		}
		cancel()
	}
}

func TestIdleWatcherDoesNotCaptureSnapshots(t *testing.T) {
	dir := repository(t)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("manual edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	client := &fakeClient{}
	runner := Runner{Root: t.TempDir(), Output: &output, Debug: true, Client: client, Interval: 20 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := runner.Run(ctx, dir, false); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte("scanning working tree")) || client.starts != 0 {
		t.Fatalf("idle watcher captured or uploaded a snapshot: %s", output.String())
	}
}

func TestCaptureRequested(t *testing.T) {
	now := time.Now()
	owner := Owner{Session: "session", Turn: "turn"}
	for _, tc := range []struct {
		name     string
		state    State
		turn     Owner
		reviewed string
		want     bool
	}{
		{name: "no request"},
		{name: "pending", state: State{Owner: owner, RequestedAt: now, Fingerprint: "new"}, turn: owner, want: true},
		{name: "reviewed", state: State{Owner: owner, RequestedAt: now, Fingerprint: "new"}, turn: owner, reviewed: "new"},
		{name: "old watcher", state: State{Owner: owner, RequestedAt: now.Add(-time.Hour), Fingerprint: "new"}, turn: owner},
		{name: "old turn", state: State{Owner: owner, RequestedAt: now, Fingerprint: "new"}, turn: Owner{Session: "session", Turn: "next"}},
		{name: "ambiguous", state: State{Owner: owner, RequestedAt: now, Fingerprint: "new", Ambiguous: true}, turn: owner},
		{name: "running tool", state: State{Active: map[string]Operation{"tool": {Owner: owner, Started: now, Attributable: true}}}, turn: owner},
		{name: "completed capture", state: State{Active: map[string]Operation{"tool": {Owner: owner, Started: now, CompletedAt: now, Attributable: true}}}, turn: owner, want: true},
		{name: "read only tool", state: State{Active: map[string]Operation{"tool": {Owner: owner, Started: now, CompletedAt: now}}}, turn: owner},
		{name: "expired capture", state: State{Active: map[string]Operation{"tool": {Owner: owner, Started: now.Add(-time.Hour), CompletedAt: now, Attributable: true}}}, turn: owner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, path := t.TempDir(), t.TempDir()
			if err := Write(StatePath(root, path), tc.state); err != nil {
				t.Fatal(err)
			}
			if err := Write(TurnPath(root, owner.Session), Turn{Owner: tc.turn}); err != nil {
				t.Fatal(err)
			}
			if got := captureRequested(root, path, now.Add(-time.Second), tc.reviewed); got != tc.want {
				t.Fatalf("captureRequested = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIdleWatcherWakesForAnAgentReview(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	client := &fakeClient{}
	var output bytes.Buffer
	runner := Runner{Root: root, Config: Config{API: "https://example.com/api"}, Token: "token", Client: client, Output: &output, Interval: 20 * time.Millisecond, StartedAt: time.Now().Add(-time.Second)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, dir, false) }()
	defer func() { cancel(); <-done }()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("agent edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	requestRunner := runner
	requestTestAgentReview(t, &requestRunner, dir)
	var state State
	if err := Read(StatePath(root, dir), &state); err != nil {
		t.Fatal(err)
	}
	state.HypothesesSubmitted = true
	if err := Write(StatePath(root, dir), state); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("idle watcher did not review the agent request")
		case <-ticker.C:
			var feed Feed
			if Read(FeedPath(root, dir), &feed) == nil && feed.AssessmentStatus == "ready" {
				return
			}
		}
	}
}
