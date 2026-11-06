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

func TestQuestionRepliesPreserveTaskForReview(t *testing.T) {
	repo, root := repository(t), t.TempDir()
	cfg := Config{Directories: []string{repo}}
	input := HookInput{Session: "chat", Turn: "original", CWD: repo, Event: "UserPromptSubmit", Prompt: "Add pagination"}
	run := func() {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		hookWithReadiness(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard, func(context.Context) bool { return false }, "codex")
	}
	run()
	for _, turnID := range []string{"first-answer", "second-answer"} {
		input.Turn = turnID
		input.Prompt = "\n<send_user_message_question_reply>\n[{\"question\":\"Continue?\",\"answer\":\"Yes\"}]\n</send_user_message_question_reply>\n"
		run()
		var turn Turn
		if err := Read(TurnPath(root, input.Session), &turn); err != nil {
			t.Fatal(err)
		}
		if turn.Prompt != "Add pagination" || turn.Owner.Turn != turnID {
			t.Fatalf("reply lost task or turn identity: %+v", turn)
		}
	}
	input.Event, input.Tool, input.ToolID = "PreToolUse", "apply_patch", "edit"
	run()
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("pagination\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input.Event = "PostToolUse"
	run()
	client := &fakeClient{}
	runner := Runner{Root: root, Config: cfg, Client: client, Output: io.Discard, StartedAt: time.Now().Add(-time.Minute)}
	if err := runner.Run(context.Background(), repo, true); err != nil {
		t.Fatal(err)
	}
	if client.starts != 1 || client.input.UserRequest == nil || client.input.UserRequest.Prompt != "Add pagination" || client.input.UserRequest.TurnID != "second-answer" {
		t.Fatalf("review lost original task or reply identity: starts=%d request=%+v", client.starts, client.input.UserRequest)
	}
	input.Event, input.Turn, input.Prompt = "UserPromptSubmit", "new-task", "Add search"
	run()
	var turn Turn
	if err := Read(TurnPath(root, input.Session), &turn); err != nil || turn.Prompt != "Add search" || turn.Owner.Turn != "new-task" {
		t.Fatalf("new task retained old prompt: %+v, %v", turn, err)
	}
}

func TestQuestionReplyPromptBoundaries(t *testing.T) {
	reply := "<send_user_message_question_reply>\n[]\n</send_user_message_question_reply>"
	for _, tc := range []struct {
		name, agent, previous, prompt, want string
	}{
		{"no original task", "codex", "", reply, ""},
		{"ordinary follow-up", "codex", "Original task", "Please add tests too", "Please add tests too"},
		{"quoted marker", "codex", "Original task", "Explain this: " + reply, "Explain this: " + reply},
		{"incomplete wrapper", "codex", "Original task", "<send_user_message_question_reply>", "<send_user_message_question_reply>"},
		{"other agent", "claude", "Original task", reply, reply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, parent := t.TempDir(), t.TempDir()
			file := TurnPath(root, "chat")
			if err := Write(file, Turn{Owner: Owner{"chat", "original"}, Prompt: tc.previous, Agent: tc.agent}); err != nil {
				t.Fatal(err)
			}
			input := HookInput{Session: "chat", Turn: "reply", CWD: parent, Event: "UserPromptSubmit", Prompt: tc.prompt}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			hookWithReadiness(context.Background(), root, Config{}, bytes.NewReader(raw), io.Discard, func(context.Context) bool { return false }, tc.agent)
			var turn Turn
			if err := Read(file, &turn); err != nil || turn.Prompt != tc.want || turn.Owner.Turn != "reply" {
				t.Fatalf("unexpected task after prompt: %+v, %v", turn, err)
			}
		})
	}
}
