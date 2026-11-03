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

func TestClaudeActivatesMidTurnWithoutRestart(t *testing.T) {
	for _, promptID := range []string{"", "native-prompt"} {
		t.Run(promptID, func(t *testing.T) {
			root, repo := t.TempDir(), repository(t)
			task := "Add pagination without changing the UI"
			if err := BeginClaudeActivation(root, repo, "session", task); err != nil {
				t.Fatal(err)
			}
			if got := ClaudeActivationStatus(root, repo, "session"); got != "waiting_for_hooks" {
				t.Fatal(got)
			}
			run := func(event string) {
				input := HookInput{Session: "session", PromptID: promptID, CWD: repo, Event: event, Tool: "Bash", ToolID: "setup", Input: json.RawMessage(`{"command":"doomer onboard"}`)}
				raw, _ := json.Marshal(input)
				hookForTest(context.Background(), root, Config{Directories: []string{repo}}, strings.NewReader(string(raw)), &strings.Builder{}, "claude")
			}
			// The installation command's PostToolUse arrives before the next command's PreToolUse.
			run("PostToolUse")
			var turn Turn
			Read(TurnPath(root, "session"), &turn)
			if turn.Prompt != task || turn.Agent != "claude" || turn.Owner.Turn == "" {
				t.Fatalf("lost current task: %+v", turn)
			}
			if got := ClaudeActivationStatus(root, repo, "session"); got != "waiting_for_hooks" {
				t.Fatal(got)
			}
			run("PreToolUse")
			if got := ClaudeActivationStatus(root, repo, "session"); got != "ready" {
				t.Fatal(got)
			}
			if got := ClaudeActivationStatus(root, repo, "other-session"); got == "ready" {
				t.Fatal("cross-session receipt")
			}
			if got := ClaudeActivationStatus(root, filepath.Join(repo, "other"), "session"); got == "ready" {
				t.Fatal("cross-repository receipt")
			}
			for _, event := range []string{"PreToolUse", "PostToolUse"} {
				if event == "PostToolUse" {
					os.WriteFile(filepath.Join(repo, "file.txt"), []byte("new code\n"), 0600)
				}
				input := HookInput{Session: "session", PromptID: promptID, CWD: repo, Event: event, Tool: "Edit", ToolID: "edit", Input: json.RawMessage(`{"file_path":"file.txt"}`)}
				raw, _ := json.Marshal(input)
				hookForTest(context.Background(), root, Config{Directories: []string{repo}}, strings.NewReader(string(raw)), &strings.Builder{}, "claude")
			}
			var state State
			Read(StatePath(root, repo), &state)
			if state.Prompt != task || state.Agent != "claude" || state.RequestedAt.IsZero() {
				t.Fatalf("live edit lost its task: %+v", state)
			}
		})
	}
}

func TestClaudeActivationDoesNotOverwriteNewPrompt(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	BeginClaudeActivation(root, repo, "session", "Old onboarding task")
	run := func(event, prompt string) {
		raw, _ := json.Marshal(HookInput{Session: "session", CWD: repo, Event: event, Prompt: prompt, Tool: "Bash", ToolID: "read", Input: json.RawMessage(`{"command":"git status --short"}`)})
		hookForTest(context.Background(), root, Config{Directories: []string{repo}}, strings.NewReader(string(raw)), &strings.Builder{}, "claude")
	}
	run("UserPromptSubmit", "A different task")
	run("PreToolUse", "")
	run("PostToolUse", "")
	var turn Turn
	Read(TurnPath(root, "session"), &turn)
	if turn.Prompt != "A different task" {
		t.Fatal(turn.Prompt)
	}
	if got := ClaudeActivationStatus(root, repo, "session"); got != "ready" {
		t.Fatal(got)
	}
	// Expired receipts are not proof that hooks still run.
	var a claudeActivation
	file := claudeActivationPath(root, repo, "session")
	Read(file, &a)
	a.Created = time.Now().Add(-claudeActivationTTL - time.Second)
	Write(file, a)
	if got := ClaudeActivationStatus(root, repo, "session"); got == "ready" {
		t.Fatal("stale receipt accepted")
	}
}

func TestClaudeActivationRequiresApprovedRepositoryAndUsableEvents(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	BeginClaudeActivation(root, repo, "session", "Task")
	for _, cfg := range []Config{{}, {Directories: []string{repo}}} {
		for _, event := range []string{"PreToolUse", "PostToolUse"} {
			input := HookInput{Session: "session", CWD: repo, Event: event, Tool: "Bash"}
			if len(cfg.Directories) == 0 {
				input.ToolID = "unapproved"
			}
			raw, _ := json.Marshal(input)
			hookForTest(context.Background(), root, cfg, strings.NewReader(string(raw)), &strings.Builder{}, "claude")
		}
	}
	if got := ClaudeActivationStatus(root, repo, "session"); got != "waiting_for_hooks" {
		t.Fatal(got)
	}
	var turn Turn
	if Read(TurnPath(root, "session"), &turn) == nil {
		t.Fatal("bound a task without permission or a usable hook event")
	}
}
