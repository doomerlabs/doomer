package watch

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestHookDiagnosticExplainsMissingIDWithoutCapturingInputs(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	input := HookInput{Session: "chat", Turn: "turn", CWD: dir, Event: "PreToolUse", Tool: "edit", Input: json.RawMessage(`{"secret":"PRIVATE_CONTENT"}`), Prompt: "PRIVATE_PROMPT"}
	raw, _ := json.Marshal(input)
	hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), io.Discard)
	var d hookDiagnostic
	if err := Read(filepath.Join(root, "hook-diagnostics", Key("chatPreToolUse")+".json"), &d); err != nil {
		t.Fatal(err)
	}
	if d.Status != "missing tool use ID" || d.Repository != dir || d.HasToolID {
		t.Fatalf("incorrect diagnostic: %+v", d)
	}
	logged, _ := json.Marshal(d)
	if strings.Contains(string(logged), "PRIVATE") {
		t.Fatal("diagnostics captured private inputs")
	}
}
