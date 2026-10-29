package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
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
		Hook(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard)
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
