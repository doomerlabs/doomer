package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyOverlapDoesNotClaimOrInvalidateEdits(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		writer       bool
		finishReader bool
	}{
		{name: "two readers"},
		{name: "reader and writer", writer: true},
		{name: "reader ends during writer", writer: true, finishReader: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			writer := scenario.writer
			repo, root := repository(t), t.TempDir()
			cfg := Config{Directories: []string{repo}}
			run := func(input HookInput) string {
				t.Helper()
				raw, _ := json.Marshal(input)
				var out bytes.Buffer
				hookWithRepositories(context.Background(), root, cfg, bytes.NewReader(raw), &out, func(context.Context) bool { return false }, true, "codex")
				if strings.Contains(out.String(), "could not start a review") {
					t.Fatalf("false conflict: %s", out.String())
				}
				return out.String()
			}
			a := HookInput{Session: "reader", Turn: "read", CWD: repo, Event: "UserPromptSubmit", Prompt: "inspect"}
			b := HookInput{Session: "other", Turn: "other", CWD: repo, Event: "UserPromptSubmit", Prompt: "work"}
			run(a)
			run(b)
			a.Event, a.Tool, a.ToolID = "PreToolUse", "Bash", "read"
			a.Input = json.RawMessage(`{"command":"cat file.txt"}`)
			b.Event, b.Tool, b.ToolID = "PreToolUse", "Bash", "other"
			b.Input = json.RawMessage(`{"command":"git status"}`)
			if writer {
				b.Input = json.RawMessage(`{"command":"printf changed > file.txt"}`)
			}
			run(a)
			run(b)
			if writer {
				if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.finishReader {
				finish(root, repo, Owner{a.Session, a.Turn}, context.Background())
			} else {
				a.Event = "PostToolUse"
				run(a)
			}
			b.Event = "PostToolUse"
			run(b)
			var state State
			if err := Read(StatePath(root, repo), &state); err != nil {
				t.Fatal(err)
			}
			if state.Ambiguous {
				t.Fatalf("read caused ambiguity: %+v", state)
			}
			if writer && (state.Owner.Session != b.Session || state.RequestedAt.IsZero()) {
				t.Fatalf("writer lost attribution: %+v", state)
			}
			if !writer && !state.RequestedAt.IsZero() {
				t.Fatal("read-only calls requested review")
			}
		})
	}
}

func TestLinkedWorktreeWritesRemainIndependent(t *testing.T) {
	repo, root := repository(t), t.TempDir()
	linked := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command("git", "-C", repo, "-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", linked, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("worktree: %s %v", out, err)
	}
	var err error
	linked, err = Root(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Directories: []string{repo, linked}}
	run := func(input HookInput) {
		raw, _ := json.Marshal(input)
		hookWithRepositories(context.Background(), root, cfg, bytes.NewReader(raw), &bytes.Buffer{}, func(context.Context) bool { return false }, true, "codex")
	}
	for i, path := range []string{repo, linked} {
		session := []string{"main-chat", "linked-chat"}[i]
		input := HookInput{Session: session, Turn: "turn", CWD: path, Event: "UserPromptSubmit", Prompt: "edit"}
		run(input)
		input.Event, input.Tool, input.ToolID = "PreToolUse", "Bash", "edit"
		input.Input = json.RawMessage(`{"command":"printf changed > file.txt"}`)
		run(input)
	}
	for i, path := range []string{repo, linked} {
		if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("changed\n"), 0600); err != nil {
			t.Fatal(err)
		}
		input := HookInput{Session: []string{"main-chat", "linked-chat"}[i], Turn: "turn", CWD: path, Event: "PostToolUse", Tool: "Bash", ToolID: "edit"}
		run(input)
		var state State
		if err := Read(StatePath(root, path), &state); err != nil {
			t.Fatal(err)
		}
		if state.Ambiguous || state.Owner.Session != input.Session || state.RequestedAt.IsZero() {
			t.Fatalf("worktrees shared ownership: %+v", state)
		}
	}
}

func TestExplicitTemporaryWorkDoesNotInvalidateReview(t *testing.T) {
	root, repo, outside := t.TempDir(), repository(t), t.TempDir()
	owner := Owner{"chat", "turn"}
	if err := Write(TurnPath(root, owner.Session), Turn{Owner: owner, Repositories: []string{repo}}); err != nil {
		t.Fatal(err)
	}
	input := HookInput{Session: owner.Session, Turn: owner.Turn, CWD: repo, Event: "PreToolUse", Tool: "Bash", ToolID: "temporary-build"}
	input.Input = json.RawMessage(`{"workdir":` + quotedJSON(outside) + `,"command":"tsc --noEmit > build.log"}`)
	raw, _ := json.Marshal(input)
	var out bytes.Buffer
	hookWithRepositories(context.Background(), root, Config{Directories: []string{repo}}, bytes.NewReader(raw), &out, func(context.Context) bool { return false }, true, "codex")
	if strings.Contains(out.String(), "could not start") {
		t.Fatalf("temporary work reported as failed review: %s", out.String())
	}
	if _, err := os.Stat(StatePath(root, repo)); !os.IsNotExist(err) {
		t.Fatal("outside work altered repository state")
	}
}

func TestHookDiagnosticHistoryIsBoundedAndRetainsConflict(t *testing.T) {
	root := t.TempDir()
	conflict := &AttributionConflict{Reason: "overlapping_writes", ToolID: "edit-a", OtherToolID: "edit-b", OtherOwner: Owner{"other-chat", "turn"}}
	for i := 0; i < 130; i++ {
		recordHookDiagnostic(root, "chat", hookDiagnostic{At: time.Now(), Event: "PreToolUse", Tool: "Bash", ToolID: "edit-a", Owner: Owner{"chat", "turn"}, Conflict: conflict})
	}
	var history []hookDiagnostic
	if err := Read(filepath.Join(root, "hook-history", Key("chat")+".json"), &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 128 || history[0].Conflict.OtherOwner.Session != "other-chat" {
		t.Fatalf("history lost conflict: %d", len(history))
	}
}

func TestHookHistoryPrunesInactiveSessionsAndPreservesActiveWriters(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-8 * 24 * time.Hour)
	for _, session := range []string{"expired", "active", "recent"} {
		recordHookDiagnostic(root, session, hookDiagnostic{At: time.Now(), Event: "PreToolUse"})
		if session != "recent" {
			file := filepath.Join(root, "hook-history", Key(session)+".json")
			if err := os.Chtimes(file, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := Locked(root, "hook-history", func() error {
		(Runner{Root: root}).logHookDiagnostics("", time.Time{})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "hook-history", Key("expired")+".json")); !os.IsNotExist(err) {
		t.Fatalf("expired history survived: %v", err)
	}
	for _, session := range []string{"active", "recent"} {
		if _, err := os.Stat(filepath.Join(root, "hook-history", Key(session)+".json")); err != nil {
			t.Fatalf("retained history %s: %v", session, err)
		}
	}
	recordHookDiagnostic(root, "active", hookDiagnostic{At: time.Now(), Event: "PostToolUse"})
	(Runner{Root: root}).logHookDiagnostics("", time.Time{})
	var history []hookDiagnostic
	if err := Read(filepath.Join(root, "hook-history", Key("active")+".json"), &history); err != nil || len(history) != 2 {
		t.Fatalf("updated history was lost: %v, %d events", err, len(history))
	}
}
