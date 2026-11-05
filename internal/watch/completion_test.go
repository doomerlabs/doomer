package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanReviewDeliveredOnceWithFullLinkAndOnlyCurrentSnapshot(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	owner := Owner{"session", "turn"}
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600)
	digest, err := snapshotDigest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner})
	Write(StatePath(root, dir), State{Owner: owner, Fingerprint: digest, HypothesesSubmitted: true, RequestedAt: time.Now()})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	url := "https://doomer.ai/look-closer/org/repo/digest#review=keep-entire-token"
	feed := Feed{Owner: owner, Fingerprint: digest, ReviewID: "review", URL: url, AssessmentStatus: "ready", Updated: time.Now()}
	run := func() string {
		raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: "Stop", StopActive: true})
		var out strings.Builder
		hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), &out)
		return out.String()
	}
	// Current matching result is delivered even with no leads.
	feed.Fingerprint = digest
	Write(FeedPath(root, dir), feed)
	got := run()
	for _, text := range []string{"no areas to check", "no longer pending", url} {
		if !strings.Contains(got, text) {
			t.Fatalf("missing %q in %s", text, got)
		}
	}
	if got := strings.TrimSpace(run()); got != "{}" {
		t.Fatalf("repeated completion: %s", got)
	}
	// A distinct revision, including a clean one, must be delivered again.
	feed.ReviewID = "review-2"
	Write(FeedPath(root, dir), feed)
	if !strings.Contains(run(), "no longer pending") {
		t.Fatal("second review was suppressed")
	}
}

func TestSavedTaskProvidedByPromptHookOnce(t *testing.T) {
	root, dir := t.TempDir(), repository(t)
	SaveTaskHandoff(root, dir, "session", strings.NewReader("Add pagination. Keep the UI unchanged."))
	raw, _ := json.Marshal(HookInput{Session: "session", Turn: "resume", CWD: dir, Event: "UserPromptSubmit", Prompt: ResumeHandoffPrompt})
	run := func() string {
		var out strings.Builder
		hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), &out)
		return out.String()
	}
	if !strings.Contains(run(), "Add pagination. Keep the UI unchanged.") {
		t.Fatal("missing saved task")
	}
	if strings.Contains(run(), "Add pagination") {
		t.Fatal("task handoff repeated")
	}
	if got := handoffPrompt(root, dir, Owner{"session", "resume"}, ResumeHandoffPrompt); !strings.Contains(got, "Add pagination") {
		t.Fatal("review lost original task")
	}
}

func TestCompletedReviewSurvivesExternalBaselineChangeWithoutClaimingCurrentCode(t *testing.T) {
	for _, withLeads := range []bool{false, true} {
		t.Run(fmt.Sprint(withLeads), func(t *testing.T) {
			dir, root := repository(t), t.TempDir()
			owner := Owner{"session", "turn"}
			os.WriteFile(filepath.Join(dir, "file.txt"), []byte("agent changed this\n"), 0600)
			digest, err := snapshotDigest(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			Write(TurnPath(root, owner.Session), Turn{Owner: owner, Next: 1})
			Write(StatePath(root, dir), State{Owner: owner, Fingerprint: digest, HypothesesSubmitted: true, RequestedAt: time.Now()})
			Write(filepath.Join(root, "heartbeat.json"), time.Now())
			feed := Feed{Owner: owner, Fingerprint: digest, ReviewID: "review", URL: "https://doomer.ai/review#complete-token", AssessmentStatus: "ready", Updated: time.Now()}
			if withLeads {
				feed.Leads = []Lead{{ID: strings.Repeat("a", 64), Path: "file.txt", Line: 1, Concern: "Check this change", Check: "Verify current behavior"}}
			}
			Write(FeedPath(root, dir), feed)
			// Reproduce a framework changing instructions outside a captured agent edit.
			os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Framework generated instructions\n"), 0600)
			run := func(event string, who Owner) string {
				raw, _ := json.Marshal(HookInput{Session: who.Session, Turn: who.Turn, CWD: dir, Event: event, Tool: "Bash", ToolID: "read-status", Input: json.RawMessage(`{"command":"git status --short"}`)})
				var out strings.Builder
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				hookForTest(ctx, root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), &out)
				return out.String()
			}
			run("PreToolUse", owner)
			output := run("PostToolUse", owner)
			if !strings.Contains(output, "checkout changed") || !strings.Contains(output, feed.URL) || strings.Contains(output, "assessment of the latest edit") {
				t.Fatal(output)
			}
			if withLeads && !strings.Contains(output, "Check this change") {
				t.Fatal(output)
			}
			var state State
			Read(StatePath(root, dir), &state)
			if state.Owner != (Owner{}) || !state.RequestedAt.IsZero() {
				t.Fatal("restored ownership of external changes")
			}
			if agentReviewRequested(root, dir, state.Fingerprint, time.Time{}) {
				t.Fatal("external change scheduled for upload")
			}
			if got := strings.TrimSpace(run("Stop", owner)); got != "{}" {
				t.Fatal("delivered twice", got)
			}
			if got := strings.TrimSpace(run("Stop", Owner{"other-session", "turn"})); got != "{}" {
				t.Fatal("cross-session delivery", got)
			}
			if got := strings.TrimSpace(run("Stop", Owner{"session", "next-turn"})); got != "{}" {
				t.Fatal("cross-turn delivery", got)
			}
		})
	}
}

func TestStopProgressWaitsSilentlyForCompletedReview(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	owner := Owner{"session", "turn"}
	digest, err := snapshotDigest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner})
	Write(StatePath(root, dir), State{Owner: owner, Fingerprint: digest, HypothesesSubmitted: true, RequestedAt: time.Now()})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	feed := Feed{Owner: owner, Fingerprint: digest, ReviewID: "review", URL: "https://example.com/review", AssessmentStatus: "analyzing", Updated: time.Now()}
	Write(FeedPath(root, dir), feed)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		time.Sleep(50 * time.Millisecond)
		feed.AssessmentStatus = "preparing"
		Write(FeedPath(root, dir), feed)
		time.Sleep(200 * time.Millisecond)
		feed.AssessmentStatus = "ready"
		Write(FeedPath(root, dir), feed)
	}()
	raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: "Stop", StopActive: true})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out strings.Builder
	hookForTest(ctx, root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), &out)
	<-finished
	if ctx.Err() != nil || !strings.Contains(out.String(), "no areas to check") || strings.Contains(out.String(), "no assessment is available") {
		t.Fatal("progress interrupted final delivery", out.String())
	}
}

func TestStopPendingReviewRespectsDeadlineWithoutFeedback(t *testing.T) {
	for _, status := range []string{"uploading", "analyzing", "preparing", "retrying", ""} {
		t.Run(status, func(t *testing.T) {
			dir, root := repository(t), t.TempDir()
			owner := Owner{"session", "turn"}
			digest, err := snapshotDigest(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			Write(TurnPath(root, owner.Session), Turn{Owner: owner})
			Write(StatePath(root, dir), State{Owner: owner, Fingerprint: digest, RequestedAt: time.Now()})
			Write(filepath.Join(root, "heartbeat.json"), time.Now())
			if status != "" {
				Write(FeedPath(root, dir), Feed{Owner: owner, Fingerprint: digest, AssessmentStatus: status, Updated: time.Now()})
			}
			raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: "Stop"})
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			var out strings.Builder
			hookForTest(ctx, root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), &out)
			if strings.TrimSpace(out.String()) != "{}" {
				t.Fatal("Stop exposed internal instructions or progress", out.String())
			}
			var state State
			Read(StatePath(root, dir), &state)
			if state.QuestionRequest != "" {
				t.Fatal("Stop created an undelivered hypothesis request")
			}
		})
	}
}
