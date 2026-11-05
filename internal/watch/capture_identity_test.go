package watch

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func commitCapturedFiles(t *testing.T, repo string) {
	t.Helper()
	for _, args := range [][]string{{"add", "."}, {"-c", "core.hooksPath=/dev/null", "commit", "-m", "test change"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %s: %v", out, err)
		}
	}
}

func TestSetupCommitDoesNotClaimExistingFiles(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	owner := Owner{"chat", "turn"}
	cfg := Config{Directories: []string{repo}}
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("already installed instructions\n"), 0600); err != nil {
		t.Fatal(err)
	}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner, Repositories: []string{repo}})
	run := func(event string) string {
		raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: repo, Event: event, Tool: "Bash", ToolID: "commit", Input: json.RawMessage(`{"command":"git add . && git commit -m setup"}`)})
		var out strings.Builder
		hookForTest(context.Background(), root, cfg, strings.NewReader(string(raw)), &out)
		return out.String()
	}
	run("PreToolUse")
	commitCapturedFiles(t, repo)
	got := run("PostToolUse")
	if !strings.Contains(got, "do not mention this hook result") || strings.Contains(got, "could not") {
		t.Fatalf("no-op was not quiet: %s", got)
	}
	var state State
	Read(StatePath(root, repo), &state)
	if state.Ambiguous || state.Owner != (Owner{}) || !state.RequestedAt.IsZero() {
		t.Fatalf("setup files were claimed: %+v", state)
	}
}

func TestCapturedEditSurvivesCommitWithoutClaimingExistingChanges(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	owner := Owner{"chat", "turn"}
	cfg := Config{Directories: []string{repo}}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner, Repositories: []string{repo}, Prompt: "update the file"})
	os.WriteFile(filepath.Join(repo, "existing.txt"), []byte("someone else's work\n"), 0600)
	os.WriteFile(filepath.Join(repo, "file.txt"), []byte("one\ntwo\nuser line\n"), 0600)
	run := func(event, id, command string) string {
		input := HookInput{Session: owner.Session, Turn: owner.Turn, CWD: repo, Event: event, Tool: "Bash", ToolID: id}
		input.Input, _ = json.Marshal(map[string]string{"command": command})
		raw, _ := json.Marshal(input)
		var out strings.Builder
		hookForTest(context.Background(), root, cfg, strings.NewReader(string(raw)), &out)
		return out.String()
	}
	run("PreToolUse", "edit", "apply_patch")
	os.WriteFile(filepath.Join(repo, "file.txt"), []byte("one\ntwo\nuser line\nagent edit\n"), 0600)
	run("PostToolUse", "edit", "apply_patch")
	var before State
	Read(StatePath(root, repo), &before)
	if before.Owner != owner || strings.Contains(before.AgentDiff, "existing.txt") || strings.Contains(before.AgentDiff, "+user line") || !strings.Contains(before.AgentDiff, "+agent edit") {
		t.Fatalf("bad scope: %+v", before)
	}
	run("PreToolUse", "commit", "git add . && git commit -m change")
	commitCapturedFiles(t, repo)
	run("PostToolUse", "commit", "git add . && git commit -m change")
	capture, err := captureForReview(context.Background(), root, repo, "")
	if err != nil || capture.Digest != before.Fingerprint {
		t.Fatalf("lost immutable capture: %v", err)
	}
	var after State
	Read(StatePath(root, repo), &after)
	if after.Ambiguous || after.Owner != owner || after.AgentDiff != before.AgentDiff {
		t.Fatalf("lost ownership: %+v", after)
	}
	client := &fakeClient{}
	runner := Runner{Root: root, Config: cfg, Client: client, Output: io.Discard, StartedAt: time.Now().Add(-time.Minute)}
	if err := runner.Run(context.Background(), repo, true); err != nil {
		t.Fatal(err)
	}
	if client.starts != 1 || client.input.AgentDiff != before.AgentDiff {
		t.Fatal("committed pending edit was not reviewed with its original scope")
	}
	// Changing even an unrelated file prevents reusing the old content identity.
	os.WriteFile(filepath.Join(repo, "existing.txt"), []byte("new external change\n"), 0600)
	changed, err := captureForReview(context.Background(), root, repo, "")
	if err != nil || changed.Digest == before.Fingerprint {
		t.Fatal("external edit reused the captured review identity")
	}
}

func TestReviewSummaryInstructionsKeepUnresolvedConcernsVisible(t *testing.T) {
	for _, want := range []string{"without leading with suggestion or dismissal counts", "recording every area", "deferred, unassessed, or unresolved", "Only claim the current changes"} {
		if !strings.Contains(reviewSummaryInstructions, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestCommitWithContentChangesStillReportsAmbiguousOwnership(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	owner := Owner{"chat", "turn"}
	cfg := Config{Directories: []string{repo}}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner, Repositories: []string{repo}})
	os.WriteFile(filepath.Join(repo, "file.txt"), []byte("existing edit\n"), 0600)
	run := func(event string) string {
		input := HookInput{Session: owner.Session, Turn: owner.Turn, CWD: repo, Event: event, Tool: "Bash", ToolID: "commit", Input: json.RawMessage(`{"command":"git commit -am change"}`)}
		raw, _ := json.Marshal(input)
		var out strings.Builder
		hookForTest(context.Background(), root, cfg, strings.NewReader(string(raw)), &out)
		return out.String()
	}
	run("PreToolUse")
	// A commit hook or an unrelated process changes content during the command.
	os.WriteFile(filepath.Join(repo, "file.txt"), []byte("formatter changed content\n"), 0600)
	commitCapturedFiles(t, repo)
	got := run("PostToolUse")
	if !strings.Contains(got, "could not start a review") || strings.Contains(got, quietToolMessage) {
		t.Fatalf("content changes treated as metadata: %s", got)
	}
}
