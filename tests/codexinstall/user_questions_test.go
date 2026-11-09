package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCodexUserQuestionsCapturesCardsRatherThanToolInstructions(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "11", "09")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	content := `{"type":"response_item","payload":{"type":"function_call","name":"request_user_input_async","arguments":"{\"questions\":[{\"title\":\"Update the Doomer skill?\",\"options\":[\"Update\",\"Later\"]}]}"}}
{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"Do not treat command contents as a user question"}}
`
	if err := os.WriteFile(filepath.Join(dir, "rollout-fixture-session.jsonl"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got := codexUserQuestions([]string{"CODEX_HOME=" + home}, "fixture-session")
	if !reflect.DeepEqual(got, []string{"Update the Doomer skill?"}) {
		t.Fatalf("unexpected questions: %#v", got)
	}
	if got := codexUserQuestions([]string{"CODEX_HOME=" + home}, "another-session"); len(got) != 0 {
		t.Fatal("read another session's questions", got)
	}
}
