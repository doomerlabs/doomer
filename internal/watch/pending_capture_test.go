package watch

import (
	"context"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestFailedPostCaptureResumesWithoutAnotherHook(t *testing.T) {
	root, path := t.TempDir(), "/repository"
	owner := Owner{Session: "chat", Turn: "turn"}
	since := time.Now().Add(-time.Second)
	state := State{Fingerprint: "before", Active: map[string]Operation{"edit": {Owner: owner, Before: "before", Started: since, Attributable: true}}}
	if err := Write(StatePath(root, path), state); err != nil {
		t.Fatal(err)
	}
	if err := Write(TurnPath(root, owner.Session), Turn{Owner: owner, Agent: "codex", Prompt: "fix it"}); err != nil {
		t.Fatal(err)
	}
	deferCompletedEdit(root, path, owner, "edit")
	capture := sourcesnapshot.Capture{Digest: "after"}
	if !resumeCompletedEdit(context.Background(), root, path, capture, since) {
		t.Fatal("completed edit was not resumed")
	}
	if !agentReviewRequested(root, path, capture.Digest, since) {
		t.Fatal("resumed edit is not eligible for upload")
	}
	state = State{}
	if err := Read(StatePath(root, path), &state); err != nil {
		t.Fatal(err)
	}
	if state.Agent != "codex" || state.Prompt != "fix it" || len(state.Active) != 0 {
		t.Fatalf("lost edit identity: %+v", state)
	}
	if resumeCompletedEdit(context.Background(), root, path, capture, since) {
		t.Fatal("edit resumed twice")
	}
}

func TestCaptureRecoveryDoesNotGuessOwnership(t *testing.T) {
	for _, scenario := range []string{"unfinished", "read-only", "other-turn", "overlap", "old-watcher", "ambiguous", "unchanged", "other-owner"} {
		t.Run(scenario, func(t *testing.T) {
			root, path := t.TempDir(), "/repository"
			owner := Owner{Session: "chat", Turn: "turn"}
			now := time.Now()
			since := now.Add(-time.Second)
			op := Operation{Owner: owner, Before: "before", Started: since, Attributable: true, CompletedAt: now}
			state := State{Active: map[string]Operation{"edit": op}}
			turn := Turn{Owner: owner}
			capture := sourcesnapshot.Capture{Digest: "after"}
			switch scenario {
			case "unfinished":
				op.CompletedAt = time.Time{}
				state.Active["edit"] = op
			case "read-only":
				op.Attributable = false
				state.Active["edit"] = op
			case "other-turn":
				turn.Owner.Turn = "new-turn"
			case "overlap":
				state.Active["other"] = op
			case "old-watcher":
				since = now.Add(time.Second)
			case "ambiguous":
				state.Ambiguous = true
			case "unchanged":
				capture.Digest = "before"
			case "other-owner":
				state.Owner = Owner{Session: "other", Turn: "other"}
			}
			if err := Write(StatePath(root, path), state); err != nil {
				t.Fatal(err)
			}
			if err := Write(TurnPath(root, owner.Session), turn); err != nil {
				t.Fatal(err)
			}
			if resumeCompletedEdit(context.Background(), root, path, capture, since) {
				t.Fatal("unattributable edit recovered")
			}
			if agentReviewRequested(root, path, capture.Digest, since) {
				t.Fatal("unattributable snapshot became reviewable")
			}
		})
	}
}
