package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func responseFixture(t *testing.T) (string, Turn) {
	t.Helper()
	root := t.TempDir()
	turn := Turn{Owner: Owner{"chat", "turn"}, ResponseToken: "response-token", Next: 3, Pending: []Pending{
		{Number: 1, Lead: Lead{ID: "lead-a"}, ReviewID: "review-a"},
		{Number: 2, Lead: Lead{ID: "lead-b"}, ReviewID: "review-b"},
	}}
	if err := Write(TurnPath(root, "chat"), turn); err != nil {
		t.Fatal(err)
	}
	return root, turn
}
func TestResponseHandoffIsReadOnlyAndBoundToOwner(t *testing.T) {
	root, turn := responseFixture(t)
	before, _ := os.ReadFile(TurnPath(root, "chat"))
	var out strings.Builder
	input := `[{"number":1,"status":"fixed","agrees":true,"reason":"Guard added and checked."},{"number":2,"status":"dismissed","agrees":false,"reason":"Existing validation handles the case."}]`
	if err := EmitResponses(root, "chat", turn.ResponseToken, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(TurnPath(root, "chat"))
	if string(before) != string(after) {
		t.Fatal("helper wrote state")
	}
	if files, _ := os.ReadDir(filepath.Join(root, "feedback")); len(files) != 0 {
		t.Fatal("helper queued upload")
	}
	raw, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"text": "Process exited with code 0\n" + out.String()}}})
	for _, wrong := range []Owner{{"other", "turn"}, {"chat", "other"}} {
		if recordResponsesOutput(root, wrong, raw) != 0 {
			t.Fatal("cross-owner capture")
		}
	}
	if n := recordResponsesOutput(root, turn.Owner, raw); n != 2 {
		t.Fatalf("recorded %d", n)
	}
	var report Feedback
	if err := Read(filepath.Join(root, "feedback", Key("review-b"+"lead-b")+".json"), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "dismissed" || report.Agrees == nil || *report.Agrees {
		t.Fatalf("lost dismissal: %+v", report)
	}
	if recordResponsesOutput(root, turn.Owner, raw) != 0 {
		t.Fatal("replay accepted")
	}
}
func TestResponseHandoffRejectsInvalidDecisions(t *testing.T) {
	root, turn := responseFixture(t)
	for _, input := range []string{
		`[]`,
		`[{"number":9,"status":"fixed","agrees":true,"reason":"verified"}]`,
		`[{"number":1,"status":"fixed","agrees":false,"reason":"verified"}]`,
		`[{"number":1,"status":"dismissed","agrees":true,"reason":"verified"}]`,
		`[{"number":1,"status":"fixed","reason":"verified"}]`,
		`[{"number":1,"status":"fixed","agrees":true,"reason":" "}]`,
		`[{"number":1,"status":"fixed","agrees":true,"reason":"ok"},{"number":1,"status":"dismissed","agrees":false,"reason":"no"}]`,
	} {
		if EmitResponses(root, "chat", turn.ResponseToken, strings.NewReader(input), &strings.Builder{}) == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if EmitResponses(root, "chat", "old-token", strings.NewReader(`[{"number":1,"status":"fixed","agrees":true,"reason":"verified"}]`), &strings.Builder{}) == nil {
		t.Fatal("stale request accepted")
	}
}
func TestResponseCaptureClosesToolOperation(t *testing.T) {
	root, turn := responseFixture(t)
	dir := repository(t)
	turn.Repositories = []string{dir}
	Write(TurnPath(root, "chat"), turn)
	cfg := Config{Directories: []string{dir}}
	input := HookInput{Session: "chat", Turn: "turn", CWD: dir, Tool: "exec_command", ToolID: "respond", Event: "PreToolUse"}
	raw, _ := json.Marshal(input)
	Hook(context.Background(), root, cfg, strings.NewReader(string(raw)), &strings.Builder{})
	var out strings.Builder
	if err := EmitResponses(root, "chat", turn.ResponseToken, strings.NewReader(`[{"number":1,"status":"fixed","agrees":true,"reason":"verified"}]`), &out); err != nil {
		t.Fatal(err)
	}
	input.Event = "PostToolUse"
	input.ToolResponse = json.RawMessage(out.String())
	raw, _ = json.Marshal(input)
	var result strings.Builder
	Hook(context.Background(), root, cfg, strings.NewReader(string(raw)), &result)
	var state State
	Read(StatePath(root, dir), &state)
	if len(state.Active) != 0 {
		t.Fatal("response left an active tool")
	}
	if !strings.Contains(result.String(), "recorded 1 responses") {
		t.Fatal(result.String())
	}
}
func TestResponseInstructionsSeparateReportingAndSummary(t *testing.T) {
	root, turn := responseFixture(t)
	text, err := responseInstructions(root, Config{Profile: "local", API: "http://localhost:3000/api"}, turn)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"watch respond", "Submit every decision, including dismissals", "one or two natural sentences naming Doomer.ai", "If all concerns were dismissed, omit", "does not access credentials"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(text, "Doomer N —") {
		t.Fatal("requires numbered chat reports")
	}
}
