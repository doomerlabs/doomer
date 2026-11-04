package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexActivationFromOrgDirectory(t *testing.T) {
	repo, root := repository(t), t.TempDir()
	cfg := Config{Directories: []string{repo}}
	input := HookInput{Session: "chat", Turn: "turn", CWD: filepath.Dir(repo), Event: "UserPromptSubmit", Prompt: "Update dependencies"}
	run := func() string {
		t.Helper()
		raw, _ := json.Marshal(input)
		var out bytes.Buffer
		hookWithRepositories(context.Background(), root, cfg, bytes.NewReader(raw), &out, func(context.Context) bool { return false }, true, "codex")
		return strings.TrimSpace(out.String())
	}
	run()
	for range 2 {
		if status, _ := CodexActivationStatus(root, repo, input.Session); status != "pending" {
			t.Fatalf("checking status without a tool hook activated it: %s", status)
		}
	}
	input.Event, input.Tool, input.ToolID = "PreToolUse", "Bash", "read-only"
	input.Input = json.RawMessage(`{"workdir":` + quotedJSON(repo) + `,"cmd":"git status --short"}`)
	if out := run(); out != "{}" {
		t.Fatalf("expected silent hook, got %s", out)
	}
	if status, detail := CodexActivationStatus(root, repo, input.Session); status != "ready" {
		t.Fatalf("real silent hook did not activate: %s %s", status, detail)
	}
	input.Event = "PostToolUse"
	run()
	if status, detail := CodexActivationStatus(root, repo, input.Session); status != "ready" {
		t.Fatalf("post hook lost activation: %s %s", status, detail)
	}
	for _, target := range []struct{ repo, session string }{{repo, "other-chat"}, {repository(t), input.Session}} {
		if status, _ := CodexActivationStatus(root, target.repo, target.session); status != "pending" {
			t.Fatalf("activation leaked across repository/session: %s", status)
		}
	}
	unapproved := repository(t)
	input.Event = "PreToolUse"
	input.Input = json.RawMessage(`{"workdir":` + quotedJSON(unapproved) + `,"cmd":"git status --short"}`)
	run()
	if status, _ := CodexActivationStatus(root, unapproved, input.Session); status != "pending" {
		t.Fatalf("unapproved repository activated: %s", status)
	}
	input.Event, input.Turn, input.Prompt = "UserPromptSubmit", "next-turn", "Another task"
	run()
	if status, _ := CodexActivationStatus(root, repo, input.Session); status != "pending" {
		t.Fatalf("old task activated new task: %s", status)
	}
}

func TestCodexActivationReportsUnreadableReceipt(t *testing.T) {
	root, repo, session := t.TempDir(), "/repo", "chat"
	file := codexHookReceiptPath(root, repo, session)
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if status, detail := CodexActivationStatus(root, repo, session); status != "error" || !strings.Contains(detail, "Cannot read hook receipt") {
		t.Fatalf("corrupt receipt returned %s %q", status, detail)
	}
}

func TestCodexActivationRejectsInvalidReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, hookStatus, prompt, want string
		age                            time.Duration
	}{
		{"fresh", "processed", "task", "ready", time.Second},
		{"expired", "processed", "task", "pending", 6 * time.Minute},
		{"future", "processed", "task", "pending", -time.Minute},
		{"capture failure", "capture failed: git unavailable", "task", "error", time.Second},
		{"missing prompt", "processed", "", "error", time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, repo := t.TempDir(), "/repo"
			owner := Owner{Session: "chat", Turn: "turn"}
			if err := Write(TurnPath(root, owner.Session), Turn{Owner: owner, Agent: "codex", Prompt: tc.prompt}); err != nil {
				t.Fatal(err)
			}
			file := codexHookReceiptPath(root, repo, owner.Session)
			if err := Write(file, codexHookReceipt{Repository: repo, Owner: owner, At: time.Now().Add(-tc.age), Status: tc.hookStatus}); err != nil {
				t.Fatal(err)
			}
			status, detail := CodexActivationStatus(root, repo, owner.Session)
			if status != tc.want || detail == "" {
				t.Fatalf("got %s %q, want %s", status, detail, tc.want)
			}
			if tc.name == "capture failure" && detail != tc.hookStatus {
				t.Fatalf("lost concrete error: %s", detail)
			}
		})
	}
}

func TestRootPreservesGitFailure(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho 'You have not agreed to the Xcode license agreements.' >&2\nexit 69\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	_, err := Root(context.Background(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "Xcode license") || !strings.Contains(err.Error(), "exit status 69") || !strings.Contains(err.Error(), filepath.Join(bin, "git")) {
		t.Fatalf("lost Git failure: %v", err)
	}
}
