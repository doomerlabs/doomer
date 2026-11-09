package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise Claude's wire format, including releases without prompt_id and
// Bash's nested stdout envelope, rather than supplying Codex turn IDs.
func TestClaudeReviewLifecycle(t *testing.T) {
	for _, promptID := range []string{"", "native-prompt"} {
		t.Run("prompt="+promptID, func(t *testing.T) {
			dir, root := repository(t), t.TempDir()
			cfg := Config{Directories: []string{dir}}
			if err := SaveTaskHandoff(root, dir, "claude-session", strings.NewReader("Add pagination")); err != nil {
				t.Fatal(err)
			}
			Write(filepath.Join(root, "heartbeat.json"), time.Now())
			run := func(event, tool, toolID string, output json.RawMessage, prompt string) string {
				input := HookInput{Session: "claude-session", PromptID: promptID, CWD: dir, Event: event, Tool: tool, ToolID: toolID, Input: json.RawMessage(`{"file_path":"file.txt","command":"echo test"}`), ToolResponse: output, Prompt: prompt}
				data, _ := json.Marshal(input)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				var out strings.Builder
				hookForTest(ctx, root, cfg, strings.NewReader(string(data)), &out, "claude")
				return out.String()
			}
			if out := run("UserPromptSubmit", "", "", nil, ResumeHandoffPrompt); !strings.Contains(out, "Add pagination") {
				t.Fatal(out)
			}
			run("PreToolUse", "Edit", "edit-1", nil, "")
			os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600)
			run("PostToolUse", "Edit", "edit-1", json.RawMessage(`{"success":true}`), "")
			var state State
			Read(StatePath(root, dir), &state)
			if state.Owner.Session != "claude-session" || state.Agent != "claude" || state.QuestionRequest == "" || !strings.Contains(state.Prompt, "Add pagination") {
				t.Fatalf("missing Claude edit/task ownership: %+v", state)
			}
			if promptID != "" && state.Owner.Turn != promptID {
				t.Fatal("lost native prompt ID")
			}
			var envelope strings.Builder
			if err := EmitHypotheses(context.Background(), root, state.QuestionRequest, cfg, strings.NewReader(`[]`), &envelope); err != nil {
				t.Fatal(err)
			}
			shellOutput, _ := json.Marshal(map[string]any{"stdout": envelope.String(), "stderr": "", "interrupted": false})
			run("PostToolUse", "Bash", "hypotheses", shellOutput, "")
			Read(StatePath(root, dir), &state)
			if !state.HypothesesSubmitted {
				t.Fatal("Claude Bash output did not record hypotheses")
			}
			url := "https://doomer.ai/look-closer/org/repo/digest#review=complete-token"
			feed := Feed{Owner: state.Owner, Fingerprint: state.Fingerprint, ReviewID: "claude-review", URL: url, AssessmentStatus: "ready", Updated: time.Now(), Leads: []Lead{{ID: strings.Repeat("a", 64), Path: "file.txt", Line: 1, Concern: "Check pagination", Check: "Check the final page"}}}
			Write(FeedPath(root, dir), feed)
			if out := run("Stop", "", "", nil, ""); !strings.Contains(out, url) || strings.Contains(out, "watch respond") {
				t.Fatal(out)
			}
			if out := run("PreToolUse", "Bash", "inspect", nil, ""); !strings.Contains(out, "Check pagination") || !strings.Contains(out, "additionalContext") {
				t.Fatal("missing private investigation context", out)
			}
			run("PostToolUse", "Bash", "inspect", nil, "")
			var turn Turn
			Read(TurnPath(root, "claude-session"), &turn)
			if len(turn.Pending) != 1 {
				t.Fatalf("pending=%d", len(turn.Pending))
			}
			envelope.Reset()
			if err := EmitResponses(root, "claude-session", turn.ResponseToken, strings.NewReader(`[{"number":1,"status":"dismissed","agrees":false,"reason":"Verified final page is complete"}]`), &envelope); err != nil {
				t.Fatal(err)
			}
			shellOutput, _ = json.Marshal(map[string]any{"stdout": envelope.String(), "stderr": ""})
			run("PostToolUse", "Bash", "response", shellOutput, "")
			Read(TurnPath(root, "claude-session"), &turn)
			if len(turn.Pending) != 0 {
				t.Fatal("Claude response not recorded")
			}
			files, _ := filepath.Glob(filepath.Join(root, "feedback", "*.json"))
			if len(files) != 1 {
				t.Fatal("missing feedback")
			}
			// Failed tools must close their operation instead of suppressing later edits.
			run("PreToolUse", "Bash", "failed-tool", nil, "")
			run("PostToolUseFailure", "Bash", "failed-tool", nil, "")
			Read(StatePath(root, dir), &state)
			if len(state.Active) != 0 {
				t.Fatal("failed tool left an active operation")
			}
		})
	}
}

func TestClaudeInstallPreservesSettingsAndOtherHooks(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home := t.TempDir()
	file := HookFile(home, "claude")
	Write(file, map[string]any{"model": "sonnet", "hooks": map[string]any{"PostToolUse": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "other-tool"}}}}}})
	for i := 0; i < 2; i++ {
		if err := Install(file, "doomer --marker-test", "--marker-test", false); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := Installed(file, "--marker-test"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	data, _ := os.ReadFile(file)
	var cfg map[string]any
	json.Unmarshal(data, &cfg)
	if cfg["model"] != "sonnet" || !strings.Contains(string(data), "other-tool") || !strings.Contains(string(data), "PostToolUseFailure") {
		t.Fatal(string(data))
	}
	if err := Install(file, "doomer --marker-test", "--marker-test", true); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(file)
	if strings.Contains(string(data), "--marker-test") || !strings.Contains(string(data), "other-tool") {
		t.Fatal(string(data))
	}
}
