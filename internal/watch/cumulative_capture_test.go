package watch

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestHookRetainsAllEditsInCurrentTurn(t *testing.T) {
	for _, command := range []string{"go get -u -t ./... && go mod tidy", "apply_patch"} {
		t.Run(command, func(t *testing.T) {
			root, repo, parent := t.TempDir(), repository(t), t.TempDir()
			cfg := Config{Directories: []string{repo}}
			input := HookInput{Session: "chat", Turn: "turn", CWD: parent, Prompt: "update dependencies", Event: "UserPromptSubmit"}
			run := func() {
				t.Helper()
				raw, _ := json.Marshal(input)
				hookForTest(context.Background(), root, cfg, strings.NewReader(string(raw)), io.Discard)
			}
			run()
			// Existing user changes must remain outside the agent's review scope.
			os.WriteFile(filepath.Join(repo, "existing.txt"), []byte("user work\n"), 0600)
			edit := func(id, tool, command, name, contents string) {
				t.Helper()
				input.Event, input.Tool, input.ToolID = "PreToolUse", tool, id
				input.Input, _ = json.Marshal(map[string]string{"workdir": repo, "command": command})
				run()
				if err := os.WriteFile(filepath.Join(repo, name), []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
				if id == "third" {
					// Exercise watcher recovery after a post-hook capture timeout.
					deferCompletedEdit(root, repo, Owner{input.Session, input.Turn}, id)
					capture, err := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Workspace: true, Path: repo})
					if err != nil {
						t.Fatal(err)
					}
					if resumed, err := resumeCompletedEdit(context.Background(), root, repo, capture, time.Now().Add(-time.Minute)); !resumed || err != nil {
						t.Fatal("deferred edit was not recovered")
					}
					return
				}
				input.Event = "PostToolUse"
				run()
			}
			tool := "Bash"
			if command == "apply_patch" {
				tool = command
			}
			edit("first", tool, command, "file.txt", "dependency update\n")
			var state State
			Read(StatePath(root, repo), &state)
			if state.Ambiguous || !strings.Contains(state.AgentDiff, "+dependency update") {
				t.Fatalf("first edit lost: %+v", state)
			}
			edit("second", "apply_patch", "apply_patch", "AGENTS.md", "metadata update\n")
			Read(StatePath(root, repo), &state)
			for _, want := range []string{"file.txt", "+dependency update", "AGENTS.md", "+metadata update"} {
				if !strings.Contains(state.AgentDiff, want) {
					t.Fatalf("missing %q in cumulative scope: %s", want, state.AgentDiff)
				}
			}
			if strings.Contains(state.AgentDiff, "existing.txt") {
				t.Fatal("included preexisting user work")
			}
			client := &fakeClient{}
			runner := Runner{Root: root, Config: cfg, Client: client, Output: io.Discard, StartedAt: time.Now().Add(-time.Minute)}
			if err := runner.Run(context.Background(), repo, true); err != nil {
				t.Fatal(err)
			}
			if client.starts != 1 || client.input.AgentDiff != state.AgentDiff {
				t.Fatalf("review submission lost cumulative scope: %+v", client.input)
			}
			// A second edit to the same file uses the original turn baseline.
			edit("third", "apply_patch", "apply_patch", "file.txt", "final dependency update\n")
			Read(StatePath(root, repo), &state)
			if !strings.Contains(state.AgentDiff, "-one") || strings.Contains(state.AgentDiff, "-dependency update") {
				t.Fatalf("wrong cumulative baseline: %s", state.AgentDiff)
			}
			// Reverting one file removes it from the net turn diff.
			edit("revert", "apply_patch", "apply_patch", "file.txt", "one\ntwo\n")
			Read(StatePath(root, repo), &state)
			if strings.Contains(state.AgentDiff, "file.txt") || !strings.Contains(state.AgentDiff, "AGENTS.md") {
				t.Fatalf("reverted edit remained in scope: %s", state.AgentDiff)
			}
			// External changes invalidate the old baseline rather than being adopted.
			os.WriteFile(filepath.Join(repo, "external.txt"), []byte("external work\n"), 0600)
			edit("fourth", "apply_patch", "apply_patch", "AGENTS.md", "new metadata\n")
			Read(StatePath(root, repo), &state)
			if strings.Contains(state.AgentDiff, "external.txt") || strings.Contains(state.AgentDiff, "file.txt") {
				t.Fatalf("stale baseline survived external change: %s", state.AgentDiff)
			}
			input.Event, input.Turn = "UserPromptSubmit", "next-turn"
			input.Input = nil
			run()
			edit("next", "apply_patch", "apply_patch", "file.txt", "next task\n")
			Read(StatePath(root, repo), &state)
			if strings.Contains(state.AgentDiff, "AGENTS.md") || !strings.Contains(state.AgentDiff, "+next task") {
				t.Fatalf("turn reused previous scope: %s", state.AgentDiff)
			}
		})
	}
}
