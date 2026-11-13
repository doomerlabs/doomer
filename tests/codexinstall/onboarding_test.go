package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestOnboardingCatalogRejectsRetiredJourneys(t *testing.T) {
	for _, name := range []string{"upgrade", "upgrade-defer", "policy-recovery", "portable-restart", "browser-onboarding", "review-preflight"} {
		if validScenario(name) {
			t.Fatalf("retired scenario is still runnable: %s", name)
		}
	}
	seen := map[string]bool{}
	for _, name := range scenarioNames {
		if seen[name] || !validScenario(name) {
			t.Fatalf("invalid catalog entry %s", name)
		}
		seen[name] = true
	}
}

func TestHookReviewAssertionsRequireSessionIdentityAndFeedback(t *testing.T) {
	f := &fixture{Findings: true, Reviews: map[string]adversarylabs.WorkspaceSubmission{"one": {GroupID: "one"}}}
	r := report{Checks: map[string]bool{}}
	j := journey{f: f, result: &r}
	if err := j.checkReviews(1, true); err != nil {
		t.Fatal(err)
	}
	if r.Checks["hook_session_identity"] || r.Checks["explicit_finding_feedback"] {
		t.Fatal("a manual or unassessed review satisfied the hook journey")
	}
	f.Reviews["one"] = adversarylabs.WorkspaceSubmission{GroupID: "one", UserRequest: &adversarylabs.WorkspaceUserRequest{SessionID: "session", TurnID: "turn"}}
	f.Feedback = map[string]map[string]any{"one": {"status": "dismissed", "reason": "verified"}}
	if err := j.checkReviews(1, true); err != nil {
		t.Fatal(err)
	}
	if !r.Checks["hook_session_identity"] || !r.Checks["explicit_finding_feedback"] {
		t.Fatal(r.Checks)
	}
}

func TestTerminalCompletionIgnoresPreviousChat(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".codex/sessions")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	since := time.Now()
	path := filepath.Join(dir, "session.jsonl")
	raw := `{"type":"event_msg","timestamp":"` + since.Add(-time.Hour).UTC().Format(time.RFC3339Nano) + `","payload":{"type":"task_complete"}}` + "\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if terminalCompleted(home, config{}, since) {
		t.Fatal("previous chat completed the current test")
	}
	raw += strings.Replace(raw, since.Add(-time.Hour).UTC().Format(time.RFC3339Nano), since.Add(time.Second).UTC().Format(time.RFC3339Nano), 1)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if !terminalCompleted(home, config{}, since) {
		t.Fatal("current completion was missed")
	}
}

func TestNodeSnapshotRetainsEntrypointWithoutCredentials(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for _, path := range []string{"bin/doomer.js", "bin/auth.json"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := sourceInventory(t.Context(), root, "node", "")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Path == "node/bin/auth.json" {
			t.Fatal("credentials entered the snapshot")
		}
		found = found || entry.Path == "node/bin/doomer.js"
	}
	if !found {
		t.Fatal("Node executable omitted from snapshot")
	}
}
