package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func pendingScopeFixture(t *testing.T) (string, string, sourcesnapshot.Capture, time.Time) {
	t.Helper()
	root, repo := t.TempDir(), repository(t)
	file := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Path: repo, Workspace: true})
	if err != nil {
		t.Fatal(err)
	}
	owner := Owner{"chat", "turn"}
	if err := saveOperationSnapshot(operationSnapshotPath(root, repo, owner, "edit"), before.Bytes); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Second)
	state := State{Fingerprint: before.Digest, Active: map[string]Operation{"edit": {Owner: owner, Before: before.Digest, Started: since, Attributable: true, CompletedAt: time.Now()}}}
	if err := Write(StatePath(root, repo), state); err != nil {
		t.Fatal(err)
	}
	if err := Write(TurnPath(root, owner.Session), Turn{Owner: owner, Agent: "codex", Prompt: "edit file"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Path: repo, Workspace: true})
	if err != nil {
		t.Fatal(err)
	}
	return root, repo, after, since
}

func TestRecoveryBaselineFailureIsTerminalAndDeliveredOnce(t *testing.T) {
	root, repo, capture, since := pendingScopeFixture(t)
	if err := os.Remove(operationSnapshotPath(root, repo, Owner{"chat", "turn"}, "edit")); err != nil {
		t.Fatal(err)
	}
	resumed, err := resumeCompletedEdit(context.Background(), root, repo, capture, since)
	if resumed || !errors.Is(err, errCaptureEditScope) {
		t.Fatalf("lost recovery failure: %v %v", resumed, err)
	}
	var state State
	var feed Feed
	Read(StatePath(root, repo), &state)
	Read(FeedPath(root, repo), &feed)
	if len(state.Active) != 0 || !state.RequestedAt.IsZero() || feed.FailureCode != "capture_edit_scope" || feed.Owner != (Owner{"chat", "turn"}) {
		t.Fatalf("failure not persisted: %+v %+v", state, feed)
	}
	if agentReviewRequested(root, repo, capture.Digest, since) {
		t.Fatal("failed capture became uploadable")
	}
	if resumed, err := resumeCompletedEdit(context.Background(), root, repo, capture, since); resumed || err != nil {
		t.Fatalf("terminal operation retried: %v", err)
	}
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	raw, _ := json.Marshal(HookInput{Session: "chat", Turn: "turn", CWD: repo, Event: "Stop"})
	var output bytes.Buffer
	hookWithReadiness(context.Background(), root, Config{Directories: []string{repo}}, bytes.NewReader(raw), &output, func(context.Context) bool { return true })
	if !strings.Contains(output.String(), "edit scope could not be captured") {
		t.Fatalf("failure not delivered: %s", output.String())
	}
	output.Reset()
	hookWithReadiness(context.Background(), root, Config{Directories: []string{repo}}, bytes.NewReader(raw), &output, func(context.Context) bool { return true })
	if strings.TrimSpace(output.String()) != "{}" {
		t.Fatalf("duplicate failure: %s", output.String())
	}
}

func TestRecoveryDiffFailuresAreBoundedAndCanRecover(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(map[bool]string{true: "recovers", false: "exhausted"}[recover], func(t *testing.T) {
			root, repo, capture, since := pendingScopeFixture(t)
			originalPath := os.Getenv("PATH")
			t.Setenv("PATH", t.TempDir())
			for attempt := 1; attempt <= 2; attempt++ {
				resumed, err := resumeCompletedEdit(context.Background(), root, repo, capture, since)
				if resumed || err != nil {
					t.Fatalf("attempt %d was not deferred: %v", attempt, err)
				}
				var state State
				Read(StatePath(root, repo), &state)
				if state.Active["edit"].CaptureAttempts != attempt {
					t.Fatal("attempt count not persisted")
				}
				if _, err := os.Stat(operationSnapshotPath(root, repo, Owner{"chat", "turn"}, "edit")); err != nil {
					t.Fatal("retry lost its snapshot", err)
				}
			}
			if recover {
				t.Setenv("PATH", originalPath)
			}
			resumed, err := resumeCompletedEdit(context.Background(), root, repo, capture, since)
			if _, statErr := os.Stat(operationSnapshotPath(root, repo, Owner{"chat", "turn"}, "edit")); !os.IsNotExist(statErr) {
				t.Fatalf("finished recovery retained its snapshot: %v", statErr)
			}
			if recover {
				if !resumed || err != nil {
					t.Fatalf("transient failure did not recover: %v", err)
				}
			} else {
				if resumed || !errors.Is(err, errCaptureEditScope) {
					t.Fatalf("exhaustion was silent: %v", err)
				}
				if again, err := resumeCompletedEdit(context.Background(), root, repo, capture, since); again || err != nil {
					t.Fatalf("exhausted operation retried: %v", err)
				}
			}
		})
	}
}

func TestCancelledRecoveryDoesNotConsumeAttempt(t *testing.T) {
	root, repo, capture, since := pendingScopeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if resumed, err := resumeCompletedEdit(ctx, root, repo, capture, since); resumed || err != nil {
		t.Fatalf("cancellation became failure: %v", err)
	}
	var state State
	Read(StatePath(root, repo), &state)
	if len(state.Active) != 1 || state.Active["edit"].CaptureAttempts != 0 {
		t.Fatal("cancellation consumed recovery")
	}
	if resumed, err := resumeCompletedEdit(context.Background(), root, repo, capture, since); !resumed || err != nil {
		t.Fatalf("cancelled recovery could not resume: %v", err)
	}
}

func TestNoopRecoveryRemovesOperationSnapshot(t *testing.T) {
	root, repo, capture, since := pendingScopeFixture(t)
	owner := Owner{"chat", "turn"}
	snapshot := operationSnapshotPath(root, repo, owner, "edit")
	if err := saveOperationSnapshot(snapshot, capture.Bytes); err != nil {
		t.Fatal(err)
	}
	var state State
	if err := Read(StatePath(root, repo), &state); err != nil {
		t.Fatal(err)
	}
	op := state.Active["edit"]
	op.Before = capture.Digest
	state.Active["edit"] = op
	state.DiffOwner, state.DiffBase = owner, capture.Digest
	if err := saveOperationSnapshot(reviewBaselinePath(root, repo, owner), capture.Bytes); err != nil {
		t.Fatal(err)
	}
	if err := Write(StatePath(root, repo), state); err != nil {
		t.Fatal(err)
	}
	// A changed capture identity with identical file content yields an empty diff.
	capture.Digest = "different-metadata"
	if resumed, err := resumeCompletedEdit(context.Background(), root, repo, capture, since); resumed || err != nil {
		t.Fatalf("no-op recovery: %v", err)
	}
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatalf("no-op snapshot retained: %v", err)
	}
}
