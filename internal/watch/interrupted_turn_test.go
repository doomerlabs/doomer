package watch

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInterruptedTurnDoesNotBlockLaterReview(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	cfg := Config{Directories: []string{dir}}
	run := func(input HookInput) {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		hookForTest(context.Background(), root, cfg, strings.NewReader(string(raw)), io.Discard)
	}
	first := HookInput{Session: "session", Turn: "first", CWD: dir, Event: "UserPromptSubmit", Prompt: "first request"}
	run(first)
	interrupted := first
	interrupted.Event, interrupted.Tool, interrupted.ToolID = "PreToolUse", "apply_patch", "interrupted"
	run(interrupted)
	// The process is interrupted after writing, with no PostToolUse or Stop.
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("partial old edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Include an older stuck operation too: this matches a session that has
	// already accumulated ambiguity across more than one prompt.
	var state State
	if err := Read(StatePath(root, dir), &state); err != nil {
		t.Fatal(err)
	}
	state.Owner = Owner{first.Session, first.Turn}
	state.Ambiguous = true
	state.Active["even-older"] = Operation{Owner: Owner{first.Session, "older"}, Started: time.Now(), Attributable: true}
	if err := Write(StatePath(root, dir), state); err != nil {
		t.Fatal(err)
	}
	snapshot := operationSnapshotPath(root, dir, state.Active["interrupted"].Owner, "interrupted")
	if err := Write(snapshot, map[string]string{"old": "snapshot"}); err != nil {
		t.Fatal(err)
	}
	next := first
	next.Turn, next.Prompt = "next", "next request"
	run(next)
	if err := Read(StatePath(root, dir), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Active) != 0 || state.Ambiguous || !state.RequestedAt.IsZero() {
		t.Fatalf("stale state: %+v", state)
	}
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatalf("snapshot not retired: %v", err)
	}
	// Observing the interrupted write alone is never enough to upload it.
	client := &fakeClient{}
	runner := Runner{Root: root, Config: cfg, Client: client, Output: io.Discard, StartedAt: time.Now().Add(-time.Minute)}
	if err := runner.Run(context.Background(), dir, true); err != nil {
		t.Fatal(err)
	}
	if client.starts != 0 {
		t.Fatal("new prompt uploaded pre-existing edits")
	}
	edit := next
	edit.Event, edit.Tool, edit.ToolID = "PreToolUse", "apply_patch", "next-edit"
	run(edit)
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("new attributable edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	edit.Event = "PostToolUse"
	run(edit)
	// A late completion of the abandoned operation must not reclaim this turn.
	interrupted.Event = "PostToolUse"
	run(interrupted)
	if err := Read(StatePath(root, dir), &state); err != nil {
		t.Fatal(err)
	}
	if state.Owner != (Owner{next.Session, next.Turn}) || state.Ambiguous || len(state.Active) != 0 {
		t.Fatalf("new edit not attributed: %+v", state)
	}
	if err := runner.Run(context.Background(), dir, true); err != nil {
		t.Fatal(err)
	}
	if client.starts != 1 || client.input.UserRequest == nil || client.input.UserRequest.Prompt != next.Prompt {
		t.Fatalf("new review missing or misattributed: starts=%d request=%+v", client.starts, client.input.UserRequest)
	}
}

func TestNewPromptPreservesCurrentAndOtherSessionOperations(t *testing.T) {
	for _, other := range []Owner{{"session", "next"}, {"other-session", "turn"}} {
		t.Run(other.Session+other.Turn, func(t *testing.T) {
			root, dir := t.TempDir(), repository(t)
			current := Owner{"session", "next"}
			state := State{Owner: current, Fingerprint: "baseline", Ambiguous: true, Active: map[string]Operation{
				"old":  {Owner: Owner{"session", "old"}, Started: time.Now()},
				"keep": {Owner: other, Started: time.Now()},
			}}
			if err := Write(StatePath(root, dir), state); err != nil {
				t.Fatal(err)
			}
			if err := retireInterruptedTurn(root, dir, current); err != nil {
				t.Fatal(err)
			}
			state = State{}
			if err := Read(StatePath(root, dir), &state); err != nil {
				t.Fatal(err)
			}
			if len(state.Active) != 1 || state.Active["keep"].Owner != other || !state.Ambiguous {
				t.Fatalf("active ownership lost: %+v", state)
			}
		})
	}
}

func TestNewPromptRecoversPreviouslyVisitedCheckout(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	owner := Owner{"session", "old"}
	if err := Write(TurnPath(root, owner.Session), Turn{Owner: owner, Repositories: []string{dir}}); err != nil {
		t.Fatal(err)
	}
	if err := Write(StatePath(root, dir), State{Owner: owner, Ambiguous: true, Active: map[string]Operation{"old": {Owner: owner, Started: time.Now()}}}); err != nil {
		t.Fatal(err)
	}
	input := HookInput{Session: owner.Session, Turn: "new", CWD: t.TempDir(), Event: "UserPromptSubmit", Prompt: "next task"}
	raw, _ := json.Marshal(input)
	hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), io.Discard)
	var state State
	if err := Read(StatePath(root, dir), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Active) != 0 || state.Ambiguous {
		t.Fatalf("old checkout still blocked: %+v", state)
	}
}
