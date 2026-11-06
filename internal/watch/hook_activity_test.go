package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReadOnlyHookActivatesApprovedLinkedWorktree(t *testing.T) {
	main := repository(t)
	linked := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command("git", "-C", main, "-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", linked, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("worktree: %s %v", out, err)
	}
	id, err := RepositoryIdentity(context.Background(), main)
	if err != nil {
		t.Fatal(err)
	}
	linked, err = Root(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"new", "idle", "removed", "unapproved"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			cfg := Config{RepositoryIDs: []string{id}}
			if scenario == "idle" {
				if err := RegisterRepository(root, linked, "agent", false, time.Now().Add(-2*RepositoryIdlePeriod)); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "removed" {
				if err := RemoveRepository(root, linked); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "unapproved" {
				cfg.RepositoryIDs = nil
			}
			input := HookInput{Session: "chat", Turn: "turn", CWD: linked, Event: "UserPromptSubmit", Prompt: "Inspect this checkout"}
			run := func() {
				t.Helper()
				raw, err := json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
				hookWithRepositories(context.Background(), root, cfg, bytes.NewReader(raw), &bytes.Buffer{}, func(context.Context) bool { return false }, true, "codex")
			}
			run()
			input.Event, input.Tool, input.ToolID = "PreToolUse", "Bash", "read"
			input.Input = json.RawMessage(`{"command":"git status --short"}`)
			run()
			var repo Repository
			_ = Read(repositoryFile(root, linked), &repo)
			wantActive := scenario == "new" || scenario == "idle"
			if repo.Active(time.Now()) != wantActive {
				t.Fatalf("checkout activity: %+v", repo)
			}
			var state State
			_ = Read(StatePath(root, linked), &state)
			if !state.RequestedAt.IsZero() {
				t.Fatal("read-only tool requested a review")
			}
			if wantActive {
				if status, detail := CodexActivationStatus(root, linked, input.Session); status != "ready" {
					t.Fatalf("activation: %s %s", status, detail)
				}
			}
		})
	}
}
