package watch

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTaskHandoffPreservesOriginalAcrossSetupAndOnlyOneEditingTurn(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	original := "Add an RSS feed. Keep the UI unchanged."
	if err := SaveTaskHandoff(root, repo, "session", strings.NewReader(original)); err != nil {
		t.Fatal(err)
	}
	// A different repository or conversation cannot consume the pending request.
	if got := handoffPrompt(root, repo, Owner{"other-owner", "first"}, "continue"); got != "continue" {
		t.Fatal(got)
	}
	if got := handoffPrompt(root, repo+"-other", Owner{"session", "first"}, "continue"); got != "continue" {
		t.Fatal(got)
	}
	var saved taskHandoff
	Read(taskHandoffPath(root, repo, "session"), &saved)
	if saved.BoundTurn != "" {
		t.Fatal("unrelated session consumed handoff")
	}
	for i := 0; i < 2; i++ {
		got := handoffPrompt(root, repo, Owner{"session", "first-edit"}, "yes restarted and trusted")
		if !strings.Contains(got, original) || !strings.Contains(got, "yes restarted and trusted") {
			t.Fatal(got)
		}
	}
	if got := handoffPrompt(root, repo, Owner{"session", "next-task"}, "Add search"); got != "Add search" {
		t.Fatal(got)
	}
}

func TestTaskHandoffRejectsOversizedAndExpiredRequests(t *testing.T) {
	root := t.TempDir()
	repo := root
	if err := SaveTaskHandoff(root, repo, "s", strings.NewReader(strings.Repeat("x", 3001))); err == nil {
		t.Fatal("oversized request accepted")
	}
	if err := SaveTaskHandoff(root, repo, "s", strings.NewReader("Original task")); err != nil {
		t.Fatal(err)
	}
	var saved taskHandoff
	file := taskHandoffPath(root, repo, "s")
	Read(file, &saved)
	saved.Created = time.Now().Add(-25 * time.Hour)
	Write(file, saved)
	if got := handoffPrompt(root, repo, Owner{"s", "turn"}, "continue"); got != "continue" {
		t.Fatal(got)
	}
}
