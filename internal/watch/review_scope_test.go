package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestScopeCaptureDistinguishesFailureFromUnchangedContent(t *testing.T) {
	repo := repository(t)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	capture, err := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Path: repo, Workspace: true})
	if err != nil {
		t.Fatal(err)
	}
	baseline := filepath.Join(t.TempDir(), "baseline.json")
	if err := saveOperationSnapshot(baseline, capture.Bytes); err != nil {
		t.Fatal(err)
	}
	patch, err := snapshotAgentDiff(context.Background(), baseline, capture.Digest, capture)
	if err != nil || patch != "" {
		t.Fatalf("unchanged content: %q %v", patch, err)
	}
	for _, file := range []string{baseline, baseline + "-missing"} {
		_, err := snapshotAgentDiff(context.Background(), file, "wrong-digest", capture)
		if !errors.Is(err, errCaptureEditScope) {
			t.Fatalf("lost baseline error: %v", err)
		}
	}
	_, err = recentAgentDiff(context.Background(), baseline, "wrong-digest", capture)
	if !errors.Is(err, errCaptureEditScope) {
		t.Fatal(err)
	}
	if _, err := os.Stat(baseline); err != nil {
		t.Fatal("failure discarded evidence", err)
	}
}

func TestHookDoesNotSubmitMetadataOnlyChange(t *testing.T) {
	repo, root := repository(t), t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("existing dirty work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Directories: []string{repo}}
	input := HookInput{Session: "chat", Turn: "turn", CWD: repo, Event: "PreToolUse", Tool: "Bash", ToolID: "branch", Input: json.RawMessage(`{"command":"git switch -c other"}`)}
	run := func() {
		raw, _ := json.Marshal(input)
		hookWithRepositories(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard, func(context.Context) bool { return false }, true, "codex")
	}
	run()
	if out, err := exec.Command("git", "-C", repo, "switch", "-c", "other").CombinedOutput(); err != nil {
		t.Fatalf("switch: %s %v", out, err)
	}
	input.Event = "PostToolUse"
	run()
	var state State
	if err := Read(StatePath(root, repo), &state); err != nil {
		t.Fatal(err)
	}
	if !state.RequestedAt.IsZero() || state.AgentDiff != "" || len(state.Active) != 0 {
		t.Fatalf("metadata-only change requested review: %+v", state)
	}
}

func TestHookReportsLostScopeWithoutRequestingReview(t *testing.T) {
	repo, root := repository(t), t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("existing dirty work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Directories: []string{repo}}
	input := HookInput{Session: "chat", Turn: "turn", CWD: repo, Event: "PreToolUse", Tool: "apply_patch", ToolID: "edit"}
	run := func() string {
		raw, _ := json.Marshal(input)
		var out bytes.Buffer
		hookWithRepositories(context.Background(), root, cfg, bytes.NewReader(raw), &out, func(context.Context) bool { return false }, true, "codex")
		return out.String()
	}
	run()
	if err := os.Remove(reviewBaselinePath(root, repo, Owner{"chat", "turn"})); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("agent change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input.Event = "PostToolUse"
	if output := run(); !strings.Contains(output, "edit scope could not be captured") {
		t.Fatal(output)
	}
	var state State
	if err := Read(StatePath(root, repo), &state); err != nil {
		t.Fatal(err)
	}
	if !state.RequestedAt.IsZero() || len(state.Active) != 0 {
		t.Fatalf("failed capture requested review: %+v", state)
	}
}

func TestRunnerRejectsLegacyEmptyScopeBeforeSubmitting(t *testing.T) {
	repo := repository(t)
	os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600)
	client := &fakeClient{}
	runner := Runner{Root: t.TempDir(), Client: client, Output: io.Discard}
	requestTestAgentReview(t, &runner, repo)
	var state State
	Read(StatePath(runner.Root, repo), &state)
	state.AgentDiff = ""
	Write(StatePath(runner.Root, repo), state)
	err := runner.Run(context.Background(), repo, true)
	if !errors.Is(err, errReviewTerminal) || client.starts != 0 {
		t.Fatalf("empty scope submitted: %v starts=%d", err, client.starts)
	}
	var feed Feed
	Read(FeedPath(runner.Root, repo), &feed)
	if feed.FailureCode != "capture_edit_scope" {
		t.Fatalf("wrong diagnostic: %+v", feed)
	}
}

type fullStatusClient struct {
	fakeClient
	root, path string
	polls      int
	replace    bool
}

func (c *fullStatusClient) GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error) {
	c.polls++
	if c.replace {
		Write(FeedPath(c.root, c.path), Feed{ReviewID: "replacement", AssessmentStatus: "analyzing"})
	}
	return adversarylabs.WorkspaceReview{ID: "review", Status: "completed", Full: &adversarylabs.WorkspaceFullReview{Enabled: true, Status: "completed"}}, nil
}

func TestFailedQuickAssessmentTracksDeeperCompletionAndProtectsNewerFeed(t *testing.T) {
	for _, replace := range []bool{false, true} {
		root, path := t.TempDir(), "repo"
		client := &fullStatusClient{root: root, path: path, replace: replace}
		runner := Runner{Root: root, Client: client}
		original := Feed{ReviewID: "review", URL: "https://example.com/review", Owner: Owner{"chat", "turn"}, Fingerprint: "fp", AssessmentStatus: "unavailable", FailureCode: "missing_review_scope", Updated: time.Now()}
		// Covers saved failures from older CLIs, which have no FullReviewStatus.
		Write(FeedPath(root, path), original)
		runner.refreshUnavailableReview(context.Background(), path)
		var feed Feed
		Read(FeedPath(root, path), &feed)
		if replace {
			if feed.ReviewID != "replacement" || feed.AssessmentStatus != "analyzing" {
				t.Fatalf("overwrote newer result: %+v", feed)
			}
		} else {
			if feed.FullReviewStatus != "completed" || feed.FailureCode != "missing_review_scope" {
				t.Fatalf("lost separate stage outcomes: %+v", feed)
			}
			message := reviewProgressMessage(feed)
			if !strings.Contains(message, "deeper review completed") || !strings.Contains(message, feed.URL) || strings.Contains(message, "raised no issues") {
				t.Fatal(message)
			}
			runner.refreshUnavailableReview(context.Background(), path)
			if client.polls != 1 {
				t.Fatal("repolled completed review")
			}
		}
	}
}

type rejectedScopeClient struct{ fakeClient }

func (c *rejectedScopeClient) StartWorkspace(context.Context, string, adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	c.starts++
	return adversarylabs.WorkspaceReview{}, adversarylabs.ErrMissingReviewScope
}
func TestServerRejectedScopeStopsRetryingAndPreservesFailureCode(t *testing.T) {
	repo := repository(t)
	os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600)
	client := &rejectedScopeClient{}
	runner := Runner{Root: t.TempDir(), Client: client, Output: io.Discard}
	requestTestAgentReview(t, &runner, repo)
	err := runner.Run(context.Background(), repo, true)
	if !errors.Is(err, errReviewTerminal) || client.starts != 1 {
		t.Fatalf("scope rejection was retryable: %v", err)
	}
	var feed Feed
	Read(FeedPath(runner.Root, repo), &feed)
	if feed.FailureCode != "missing_review_scope" {
		t.Fatalf("wrong error: %+v", feed)
	}
}

func TestFailedQuickAssessmentReportsDeeperStageSeparately(t *testing.T) {
	for _, status := range []string{"running", "completed", "failed", "cancelled"} {
		message := reviewProgressMessage(Feed{AssessmentStatus: "unavailable", FailureCode: "missing_review_scope", FullReviewStatus: status, URL: "https://example.com/review"})
		if !strings.Contains(message, status) || !strings.Contains(message, "https://example.com/review") || strings.Contains(message, "raised no issues") {
			t.Fatalf("%s: %s", status, message)
		}
	}
}
