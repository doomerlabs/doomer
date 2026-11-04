package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestUnassignedPromptIsClearedOnStopOrExpiry(t *testing.T) {
	for _, stopped := range []bool{true, false} {
		root := t.TempDir()
		file := TurnPath(root, "chat")
		deadline := time.Now().Add(time.Hour)
		if err := Write(file, Turn{Owner: Owner{Session: "chat", Turn: "turn"}, Prompt: "private task", PendingPromptUntil: deadline}); err != nil {
			t.Fatal(err)
		}
		if stopped {
			clearPendingPrompt(root, file, time.Now(), true)
		} else {
			PrunePendingPrompts(root, deadline.Add(time.Second))
		}
		var got Turn
		if err := Read(file, &got); err != nil || got.Prompt != "" || !got.PendingPromptUntil.IsZero() {
			t.Fatalf("prompt retained: %+v %v", got, err)
		}
	}
}

func TestExpiredParentPromptIsNotAttachedToApprovedEdit(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	file := TurnPath(root, "chat")
	Write(file, Turn{Owner: Owner{Session: "chat", Turn: "turn"}, Prompt: "expired task", PendingPromptUntil: time.Now().Add(-time.Minute)})
	input := HookInput{Session: "chat", Turn: "turn", CWD: repo, Event: "PreToolUse", Tool: "Edit", ToolID: "edit", Input: json.RawMessage(`{"file_path":"` + filepath.Join(repo, "file.txt") + `"}`)}
	raw, _ := json.Marshal(input)
	hookWithReadiness(context.Background(), root, Config{Directories: []string{repo}}, bytes.NewReader(raw), io.Discard, func(context.Context) bool { return false }, "codex")
	var got Turn
	Read(file, &got)
	if got.Prompt != "" {
		t.Fatal("expired prompt attached", got.Prompt)
	}
}

func TestAssignedPromptSurvivesCleanup(t *testing.T) {
	root := t.TempDir()
	file := TurnPath(root, "chat")
	Write(file, Turn{Owner: Owner{Session: "chat", Turn: "turn"}, Prompt: "approved task", Repositories: []string{"repo"}})
	PrunePendingPrompts(root, time.Now().Add(24*time.Hour))
	clearPendingPrompt(root, file, time.Now(), true)
	var got Turn
	Read(file, &got)
	if got.Prompt != "approved task" {
		t.Fatal("assigned prompt lost")
	}
}
