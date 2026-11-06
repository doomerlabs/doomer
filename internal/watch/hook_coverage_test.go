package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
)

func coverageHook(t *testing.T, root string, cfg Config, input HookInput) string {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	hookWithRepositories(context.Background(), root, cfg, strings.NewReader(string(raw)), &out, func(context.Context) bool { return true }, true, "codex")
	return out.String()
}

func TestUnresolvedOperationRecoveryRequiresPreEditCapture(t *testing.T) {
	for _, scenario := range []string{"corrected-before-edit", "different-operation", "already-completed", "capture-failed"} {
		t.Run(scenario, func(t *testing.T) {
			root, repo, parent := t.TempDir(), repository(t), t.TempDir()
			owner := Owner{"chat", "turn"}
			turnFile := TurnPath(root, owner.Session)
			if err := Write(turnFile, Turn{Owner: owner, Prompt: "edit"}); err != nil {
				t.Fatal(err)
			}
			cfg := Config{Directories: []string{repo}}
			input := HookInput{Session: owner.Session, Turn: owner.Turn, CWD: parent, Event: "PreToolUse", Tool: "Bash", ToolID: "original", Input: json.RawMessage(`{"command":"python3 -c 'Path(\"file\").write_text(\"changed\")'"}`)}
			if output := coverageHook(t, root, cfg, input); !strings.Contains(output, "original") || !strings.Contains(output, "repository could not be identified") {
				t.Fatalf("missing operation diagnostic: %s", output)
			}
			if output := coverageHook(t, root, cfg, input); strings.Contains(output, "repository could not be identified") {
				t.Fatalf("replayed failure repeated notice: %s", output)
			}
			if scenario == "already-completed" {
				input.Event = "PostToolUse"
				coverageHook(t, root, cfg, input)
				input.Event = "PreToolUse"
			}
			if scenario == "different-operation" {
				input.ToolID = "later"
			}
			if scenario == "capture-failed" {
				if err := os.WriteFile(filepath.Join(repo, "pointer"), []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 1\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			input.CWD = repo
			output := coverageHook(t, root, cfg, input)
			var turn Turn
			if err := Read(turnFile, &turn); err != nil {
				t.Fatal(err)
			}
			if scenario == "corrected-before-edit" {
				if len(turn.UnresolvedOperations) != 0 || !strings.Contains(output, "recovered repository attribution") {
					t.Fatalf("corrected pre-edit capture did not recover: %+v %s", turn, output)
				}
			} else if len(turn.UnresolvedOperations) != 1 || turn.UnresolvedOperations[0].ToolID != "original" || strings.Contains(output, "recovered repository attribution") {
				t.Fatalf("uncovered operation was cleared: %+v %s", turn, output)
			}
		})
	}
}

func TestUnresolvedCoverageSurvivesCompletedReviews(t *testing.T) {
	root, parent := t.TempDir(), t.TempDir()
	repositories := []string{repository(t), repository(t)}
	owner := Owner{"chat", "turn"}
	turnFile := TurnPath(root, owner.Session)
	if err := Write(turnFile, Turn{Owner: owner, Prompt: "edit", Repositories: repositories}); err != nil {
		t.Fatal(err)
	}
	input := HookInput{Session: owner.Session, Turn: owner.Turn, CWD: parent, Event: "PreToolUse", Tool: "Bash", ToolID: "unidentified-edit", Input: json.RawMessage(`{"command":"printf changed > file.txt"}`)}
	cfg := Config{Directories: repositories}
	coverageHook(t, root, cfg, input)
	for _, repo := range repositories {
		if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600); err != nil {
			t.Fatal(err)
		}
		fingerprint, err := snapshotDigest(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		if err := Write(StatePath(root, repo), State{Owner: owner, Fingerprint: fingerprint, HypothesesSubmitted: true}); err != nil {
			t.Fatal(err)
		}
		if err := Write(FeedPath(root, repo), Feed{Owner: owner, Fingerprint: fingerprint, ReviewID: repo, URL: "https://example.com/" + filepath.Base(repo) + Key(repo), AssessmentStatus: "ready", Updated: time.Now(), Leads: []Lead{{ID: repo, Path: "file.txt", Line: 1, Concern: "check the edit"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Write(filepath.Join(root, "heartbeat.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	input.Event, input.Input = "Stop", nil
	output := coverageHook(t, root, cfg, input)
	if !strings.Contains(output, "unidentified-edit") || !strings.Contains(output, "separately from any completed reviews") || strings.Count(output, "https://example.com/") != 2 {
		t.Fatalf("Stop lost review results or scoped gap: %s", output)
	}
	var turn Turn
	if err := Read(turnFile, &turn); err != nil {
		t.Fatal(err)
	}
	next := continueReviewContext(turn, Turn{Owner: Owner{owner.Session, "continuation"}}, turn.ReviewContinuation)
	if len(next.UnresolvedOperations) != 1 || len(next.Pending) != 2 || !strings.Contains(next.ReviewContinuation, "unidentified-edit") {
		t.Fatalf("combined Stop continuation lost review or coverage state: %+v", next)
	}
	if output = coverageHook(t, root, cfg, input); strings.Contains(output, "unidentified-edit") {
		t.Fatalf("Stop repeated coverage notice: %s", output)
	}
}

func TestUnresolvedOperationsConcurrentUpdates(t *testing.T) {
	root := t.TempDir()
	owner := Owner{"chat", "turn"}
	turnFile := TurnPath(root, owner.Session)
	if err := Write(turnFile, Turn{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for _, id := range []string{"first", "second", "third", "fourth"} {
		workers.Go(func() {
			input := HookInput{ToolID: id, Tool: "Bash", CWD: "/outside"}
			recordUnresolvedOperation(context.Background(), root, turnFile, owner, input)
			input.Event = "PostToolUse"
			reconcileUnresolvedOperation(context.Background(), root, turnFile, owner, input)
		})
	}
	workers.Wait()
	var turn Turn
	if err := Read(turnFile, &turn); err != nil || len(turn.UnresolvedOperations) != 4 {
		t.Fatalf("concurrent operations lost state: %+v %v", turn, err)
	}
	for _, operation := range turn.UnresolvedOperations {
		if !operation.Completed {
			t.Fatalf("completion was lost: %+v", operation)
		}
	}
}

func TestCoverageLockHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	lock, err := publock.Acquire(root, "chat")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err = coverageLocked(ctx, root, "chat", func() error {
		t.Fatal("updated state while another callback held the lock")
		return nil
	})
	if err != context.DeadlineExceeded {
		t.Fatalf("lock did not honor cancellation: %v", err)
	}
}

func TestUnresolvedCoverageWithoutRepository(t *testing.T) {
	root, parent := t.TempDir(), t.TempDir()
	owner := Owner{"chat", "turn"}
	if err := Write(TurnPath(root, owner.Session), Turn{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	input := HookInput{Session: owner.Session, Turn: owner.Turn, CWD: parent, Event: "PreToolUse", Tool: "Bash", ToolID: "unknown", Input: json.RawMessage(`{"command":"printf changed > relative.txt"}`)}
	coverageHook(t, root, Config{}, input)
	input.Event = "Stop"
	if output := coverageHook(t, root, Config{}, input); !strings.Contains(output, "unknown") || !strings.Contains(output, "review coverage remains unverified") {
		t.Fatalf("missing coverage notice without a repository: %s", output)
	}
}

func TestUnresolvedOperationsWithoutIDsAndAcrossOwners(t *testing.T) {
	root := t.TempDir()
	owner := Owner{"chat", "turn"}
	turnFile := TurnPath(root, owner.Session)
	if err := Write(turnFile, Turn{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	input := HookInput{Tool: "Bash", CWD: t.TempDir(), Input: json.RawMessage(`{"command":"first"}`)}
	recordUnresolvedOperation(context.Background(), root, turnFile, owner, input)
	input.Input = json.RawMessage(`{"command":"second"}`)
	recordUnresolvedOperation(context.Background(), root, turnFile, owner, input)
	recordUnresolvedOperation(context.Background(), root, turnFile, owner, input)
	input.Event = "PreToolUse"
	reconcileUnresolvedOperation(context.Background(), root, turnFile, owner, input)
	var turn Turn
	if err := Read(turnFile, &turn); err != nil || len(turn.UnresolvedOperations) != 2 {
		t.Fatalf("missing-ID operations collided: %+v %v", turn, err)
	}
	other := Owner{"chat", "another-turn"}
	input.ToolID = turn.UnresolvedOperations[0].ToolID
	if message := reconcileUnresolvedOperation(context.Background(), root, turnFile, other, input); message != "" {
		t.Fatal("another turn recovered an operation")
	}
	if message := unresolvedCoverageNotice(context.Background(), root, turnFile, other); message != "" {
		t.Fatal("another turn received a coverage notice")
	}
	turn.ReviewContext, turn.ReviewContinuation = "review instructions", "continue"
	next := continueReviewContext(turn, Turn{Owner: other}, "continue")
	if len(next.UnresolvedOperations) != 2 {
		t.Fatal("review continuation lost unresolved operations")
	}
	next = continueReviewContext(turn, Turn{Owner: other}, "new task")
	if len(next.UnresolvedOperations) != 0 {
		t.Fatal("unrelated task inherited old coverage gaps")
	}
}
