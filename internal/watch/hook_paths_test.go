package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHookRepositoryFindsEditedRepoOutsideChatDirectory(t *testing.T) {
	repo, parent := repository(t), t.TempDir()
	patch := "*** Begin Patch\n*** Update File: " + filepath.Join(repo, "file.txt") + "\n@@\n-one\n+two\n*** End Patch"
	for _, input := range []any{
		map[string]any{"command": patch},
		patch,
		map[string]any{"workdir": repo, "command": "git status"},
		map[string]any{"code": "text(await tools.exec_command({workdir: " + quotedJSON(repo) + ", cmd: 'git status'}))"},
		map[string]any{"code": "text(await tools.apply_patch(" + quotedJSON(patch) + "))"},
		map[string]any{"command": "*** Begin Patch\n*** Add File: " + filepath.Join(repo, "new-directory", "new.txt") + "\n+new\n*** End Patch"},
	} {
		raw, _ := json.Marshal(input)
		got, err := hookRepository(context.Background(), HookInput{CWD: parent, Input: raw})
		if err != nil || got != repo {
			t.Fatalf("input=%s root=%s error=%v", raw, got, err)
		}
	}
}

func quotedJSON(value string) string { b, _ := json.Marshal(value); return string(b) }

func TestHookRecordsTurnAndEditWhenChatIsOutsideRepository(t *testing.T) {
	repo, parent, root := repository(t), t.TempDir(), t.TempDir()
	cfg := Config{AllRepositories: true}
	input := HookInput{Session: "chat", Turn: "turn", CWD: parent, Event: "UserPromptSubmit", Prompt: "edit readme"}
	run := func() {
		raw, _ := json.Marshal(input)
		hookForTest(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard)
	}
	run()
	input.Event = "PreToolUse"
	input.Tool = "apply_patch"
	input.ToolID = "patch"
	input.Input = json.RawMessage(quotedJSON("*** Begin Patch\n*** Update File: " + filepath.Join(repo, "file.txt") + "\n@@\n-one\n+two\n*** End Patch"))
	run()
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input.Event = "PostToolUse"
	run()
	var state State
	if err := Read(StatePath(root, repo), &state); err != nil {
		t.Fatal(err)
	}
	if state.Owner.Session != "chat" || state.Fingerprint == "" || state.RequestedAt.IsZero() {
		t.Fatalf("edit was not recorded: %+v", state)
	}
	var turn Turn
	if err := Read(TurnPath(root, "chat"), &turn); err != nil {
		t.Fatal(err)
	}
	if len(turn.Repositories) != 1 || turn.Repositories[0] != repo {
		t.Fatalf("missing turn repository: %+v", turn)
	}
}

func TestParentPromptReachesOnlyApprovedRepositoryEdits(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			repo, parent, root := repository(t), t.TempDir(), t.TempDir()
			cfg := Config{Directories: []string{repo}}
			input := HookInput{Session: "chat", Turn: "turn", CWD: parent, Event: "UserPromptSubmit", Prompt: "Update the integration docs"}
			run := func() {
				t.Helper()
				raw, _ := json.Marshal(input)
				hookWithRepositories(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard, func(context.Context) bool { return false }, true, agent)
			}
			run()
			var turn Turn
			if err := Read(TurnPath(root, input.Session), &turn); err != nil || turn.Prompt != input.Prompt {
				t.Fatalf("parent prompt was lost: %+v, %v", turn, err)
			}
			client := &fakeClient{}
			runner := Runner{Root: root, Config: cfg, Client: client, Output: io.Discard, StartedAt: time.Now().Add(-time.Minute)}
			if err := runner.Run(context.Background(), repo, true); err != nil || client.starts != 0 {
				t.Fatalf("prompt alone submitted a review: %d, %v", client.starts, err)
			}

			// The same turn can touch an unapproved checkout without attaching
			// its prompt or creating review state for that checkout.
			unapproved := repository(t)
			input.Event, input.Tool, input.ToolID = "PreToolUse", "apply_patch", "unapproved"
			input.Input = json.RawMessage(quotedJSON("*** Begin Patch\n*** Update File: " + filepath.Join(unapproved, "file.txt") + "\n@@\n-one\n+private\n*** End Patch"))
			run()
			if _, err := os.Stat(StatePath(root, unapproved)); !os.IsNotExist(err) {
				t.Fatalf("unapproved checkout gained review state: %v", err)
			}

			input.ToolID = "approved"
			input.Input = json.RawMessage(quotedJSON("*** Begin Patch\n*** Update File: " + filepath.Join(repo, "file.txt") + "\n@@\n-one\n+updated\n*** End Patch"))
			run()
			if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("updated\n"), 0600); err != nil {
				t.Fatal(err)
			}
			input.Event = "PostToolUse"
			run()
			if err := runner.Run(context.Background(), repo, true); err != nil {
				t.Fatal(err)
			}
			if client.starts != 1 || client.input.UserRequest == nil || client.input.UserRequest.Prompt != input.Prompt || client.input.UserRequest.TurnID != "turn" {
				t.Fatalf("approved review lost prompt identity: starts=%d request=%+v", client.starts, client.input.UserRequest)
			}

			// A new turn replaces the locally held prompt, even outside Git.
			input.Event, input.Turn, input.Prompt = "UserPromptSubmit", "next", "A different task"
			input.Input = nil
			run()
			if err := Read(TurnPath(root, input.Session), &turn); err != nil || turn.Prompt != input.Prompt || turn.Owner.Turn != "next" {
				t.Fatalf("new turn retained stale prompt: %+v, %v", turn, err)
			}
		})
	}
}
