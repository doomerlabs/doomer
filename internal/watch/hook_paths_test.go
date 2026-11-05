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

func TestHookRepositoryFindsEditedRepoOutsideChatDirectory(t *testing.T) {
	repo, parent := repository(t), t.TempDir()
	patch := "*** Begin Patch\n*** Update File: " + filepath.Join(repo, "file.txt") + "\n@@\n-one\n+two\n*** End Patch"
	for _, input := range []any{
		map[string]any{"command": patch},
		patch,
		map[string]any{"workdir": repo, "command": "git status"},
		map[string]any{"code": "text(await tools.exec_command({workdir: " + quotedJSON(repo) + ", cmd: 'git status'}))"},
		map[string]any{"code": "text(await tools.apply_patch(" + quotedJSON(patch) + "))"},
		map[string]any{"command": "*** Begin Patch\n*** Add File: " + filepath.Join(repo, "new-directory", "new.txt") + "\n+new\n*** End Patch"},
	} {
		raw, _ := json.Marshal(input)
		got, err := HookRepository(context.Background(), HookInput{CWD: parent, Input: raw})
		if err != nil || got != repo {
			t.Fatalf("input=%s root=%s error=%v", raw, got, err)
		}
	}
}

func quotedJSON(value string) string { b, _ := json.Marshal(value); return string(b) }

func TestHookRecordsTurnAndEditWhenChatIsOutsideRepository(t *testing.T) {
	repo, parent, root := repository(t), t.TempDir(), t.TempDir()
	cfg := Config{AllRepositories: true}
	input := HookInput{Session: "chat", Turn: "turn", CWD: parent, Event: "UserPromptSubmit", Prompt: "edit readme"}
	run := func() {
		raw, _ := json.Marshal(input)
		hookForTest(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard)
	}
	run()
	input.Event = "PreToolUse"
	input.Tool = "apply_patch"
	input.ToolID = "patch"
	input.Input = json.RawMessage(quotedJSON("*** Begin Patch\n*** Update File: " + filepath.Join(repo, "file.txt") + "\n@@\n-one\n+two\n*** End Patch"))
	run()
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("two\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input.Event = "PostToolUse"
	run()
	var state State
	if err := Read(StatePath(root, repo), &state); err != nil {
		t.Fatal(err)
	}
	if state.Owner.Session != "chat" || state.Fingerprint == "" || state.RequestedAt.IsZero() {
		t.Fatalf("edit was not recorded: %+v", state)
	}
	var turn Turn
	if err := Read(TurnPath(root, "chat"), &turn); err != nil {
		t.Fatal(err)
	}
	if len(turn.Repositories) != 1 || turn.Repositories[0] != repo {
		t.Fatalf("missing turn repository: %+v", turn)
	}
}

func TestParentPromptReachesOnlyApprovedRepositoryEdits(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			repo, parent, root := repository(t), t.TempDir(), t.TempDir()
			cfg := Config{Directories: []string{repo}}
			input := HookInput{Session: "chat", Turn: "turn", CWD: parent, Event: "UserPromptSubmit", Prompt: "Update the integration docs"}
			run := func() {
				t.Helper()
				raw, _ := json.Marshal(input)
				hookWithRepositories(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard, func(context.Context) bool { return false }, true, agent)
			}
			run()
			var turn Turn
			if err := Read(TurnPath(root, input.Session), &turn); err != nil || turn.Prompt != input.Prompt {
				t.Fatalf("parent prompt was lost: %+v, %v", turn, err)
			}
			client := &fakeClient{}
			runner := Runner{Root: root, Config: cfg, Client: client, Output: io.Discard, StartedAt: time.Now().Add(-time.Minute)}
			if err := runner.Run(context.Background(), repo, true); err != nil || client.starts != 0 {
				t.Fatalf("prompt alone submitted a review: %d, %v", client.starts, err)
			}

			// The same turn can touch an unapproved checkout without attaching
			// its prompt or creating review state for that checkout.
			unapproved := repository(t)
			input.Event, input.Tool, input.ToolID = "PreToolUse", "apply_patch", "unapproved"
			input.Input = json.RawMessage(quotedJSON("*** Begin Patch\n*** Update File: " + filepath.Join(unapproved, "file.txt") + "\n@@\n-one\n+private\n*** End Patch"))
			run()
			if _, err := os.Stat(StatePath(root, unapproved)); !os.IsNotExist(err) {
				t.Fatalf("unapproved checkout gained review state: %v", err)
			}

			input.ToolID = "approved"
			input.Input = json.RawMessage(quotedJSON("*** Begin Patch\n*** Update File: " + filepath.Join(repo, "file.txt") + "\n@@\n-one\n+updated\n*** End Patch"))
			run()
			if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("updated\n"), 0600); err != nil {
				t.Fatal(err)
			}
			input.Event = "PostToolUse"
			run()
			if err := runner.Run(context.Background(), repo, true); err != nil {
				t.Fatal(err)
			}
			if client.starts != 1 || client.input.UserRequest == nil || client.input.UserRequest.Prompt != input.Prompt || client.input.UserRequest.TurnID != "turn" {
				t.Fatalf("approved review lost prompt identity: starts=%d request=%+v", client.starts, client.input.UserRequest)
			}

			// A new turn replaces the locally held prompt, even outside Git.
			input.Event, input.Turn, input.Prompt = "UserPromptSubmit", "next", "A different task"
			input.Input = nil
			run()
			if err := Read(TurnPath(root, input.Session), &turn); err != nil || turn.Prompt != input.Prompt || turn.Owner.Turn != "next" {
				t.Fatalf("new turn retained stale prompt: %+v, %v", turn, err)
			}
		})
	}
}

func TestOrgTurnPathlessToolRetainsCapture(t *testing.T) {
	repo, parent, root := repository(t), t.TempDir(), t.TempDir()
	owner := Owner{"org-chat", "turn"}
	cfg := Config{Directories: []string{repo}}
	input := HookInput{Session: owner.Session, Turn: owner.Turn, CWD: parent, Event: "UserPromptSubmit", Prompt: "update file"}
	ready := false
	run := func() string {
		t.Helper()
		raw, _ := json.Marshal(input)
		var out bytes.Buffer
		hookWithRepositories(context.Background(), root, cfg, bytes.NewReader(raw), &out, func(context.Context) bool { return ready }, true, "codex")
		return out.String()
	}
	run()
	input.Event, input.Tool, input.ToolID = "PreToolUse", "Bash", "select"
	input.Input = json.RawMessage(`{"workdir":` + quotedJSON(repo) + `,"command":"git status --short"}`)
	run()
	input.Event = "PostToolUse"
	run()
	input.Event, input.ToolID = "PreToolUse", "edit"
	input.Input = json.RawMessage(`{"command":"printf changed > file.txt"}`)
	run()
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input.Event = "PostToolUse"
	run()
	fingerprint, err := snapshotDigest(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !agentReviewRequested(root, repo, fingerprint, time.Time{}) {
		t.Fatal("pathless edit lost the current turn's selected repository")
	}
	var state State
	if err := Read(StatePath(root, repo), &state); err != nil {
		t.Fatal(err)
	}
	state.HypothesesSubmitted = true
	Write(StatePath(root, repo), state)
	ready = true
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	Write(FeedPath(root, repo), Feed{Owner: owner, Fingerprint: fingerprint, ReviewID: "review", URL: "https://example.com/review", AssessmentStatus: "ready", Updated: time.Now()})
	input.Event = "Stop"
	if output := run(); !bytes.Contains([]byte(output), []byte("https://example.com/review")) {
		t.Fatalf("org-level Stop lost completed review: %s", output)
	}
}

func TestOrgFallbackNeverGuessesRepository(t *testing.T) {
	for _, scenario := range []string{"previous-turn", "multiple-repositories", "explicit-unapproved", "explicit-invalid"} {
		t.Run(scenario, func(t *testing.T) {
			repo, other, parent, root := repository(t), repository(t), t.TempDir(), t.TempDir()
			owner := Owner{"chat", "current"}
			turn := Turn{Owner: owner, Agent: "codex", Prompt: "edit", Repositories: []string{repo}}
			input := HookInput{Session: owner.Session, Turn: owner.Turn, CWD: parent, Event: "PreToolUse", Tool: "Bash", ToolID: "edit", Input: json.RawMessage(`{"command":"printf changed > file.txt"}`)}
			switch scenario {
			case "previous-turn":
				turn.Owner.Turn = "previous"
			case "multiple-repositories":
				turn.Repositories = append(turn.Repositories, other)
			case "explicit-unapproved":
				input.Input = json.RawMessage(`{"workdir":` + quotedJSON(other) + `,"command":"printf changed > file.txt"}`)
			case "explicit-invalid":
				input.Input = json.RawMessage(`{"workdir":` + quotedJSON(parent) + `,"command":"printf changed > file.txt"}`)
			}
			if err := Write(TurnPath(root, owner.Session), turn); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(input)
			var out bytes.Buffer
			hookWithRepositories(context.Background(), root, Config{Directories: []string{repo}}, bytes.NewReader(raw), &out, func(context.Context) bool { return false }, true, "codex")
			for _, candidate := range []string{repo, other} {
				if _, err := os.Stat(StatePath(root, candidate)); !os.IsNotExist(err) {
					t.Fatalf("unsafe fallback created state for %s: %v", candidate, err)
				}
			}
			if scenario == "multiple-repositories" {
				if !bytes.Contains(out.Bytes(), []byte("repository could not be identified")) {
					t.Fatalf("missing routing diagnostic: %s", out.String())
				}
			}
		})
	}
}

func TestOverlappingChatsReportCaptureFailureOnce(t *testing.T) {
	repo, root := repository(t), t.TempDir()
	cfg := Config{Directories: []string{repo}}
	input := HookInput{Session: "first", Turn: "turn", CWD: repo, Event: "UserPromptSubmit", Prompt: "edit"}
	run := func() string {
		t.Helper()
		raw, _ := json.Marshal(input)
		var out bytes.Buffer
		hookWithRepositories(context.Background(), root, cfg, bytes.NewReader(raw), &out, func(context.Context) bool { return false }, true, "codex")
		return out.String()
	}
	run()
	input.Event, input.Tool, input.ToolID = "PreToolUse", "Bash", "first-edit"
	input.Input = json.RawMessage(`{"command":"printf first > file.txt"}`)
	run()
	input.Session, input.Event = "second", "UserPromptSubmit"
	run()
	input.Event, input.ToolID = "PreToolUse", "second-edit"
	if output := run(); !bytes.Contains([]byte(output), []byte("separate worktrees")) {
		t.Fatalf("overlapping ownership was silent: %s", output)
	}
	if output := run(); bytes.Contains([]byte(output), []byte("separate worktrees")) {
		t.Fatalf("repeated ownership warning: %s", output)
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input.Event = "PostToolUse"
	run()
	fingerprint, _ := snapshotDigest(context.Background(), repo)
	if agentReviewRequested(root, repo, fingerprint, time.Time{}) {
		t.Fatal("overlapping chats acquired upload ownership")
	}
}
