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
	for _, text := range []string{"raised no issues", url} {
		if !strings.Contains(got, text) {
			t.Fatalf("missing %q in %s", text, got)
		}
	}
	for _, internal := range []string{"Briefly say", "Include [", "Do not repeat", "final answer", "internal handoff"} {
		if strings.Contains(got, internal) {
			t.Fatalf("visible completion contains private instructions: %s", got)
		}
	}
	if got := strings.TrimSpace(run()); got != "{}" {
		t.Fatalf("repeated completion: %s", got)
	}
	// A distinct revision, including a clean one, must be delivered again.
	feed.ReviewID = "review-2"
	Write(FeedPath(root, dir), feed)
	if !strings.Contains(run(), "raised no issues") {
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
	if ctx.Err() != nil || !strings.Contains(out.String(), "raised no issues") || strings.Contains(out.String(), "no assessment is available") {
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

func TestStopQueuesInvestigationInPrivateToolContext(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			dir, root := repository(t), t.TempDir()
			owner := Owner{"session", "turn"}
			if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600); err != nil {
				t.Fatal(err)
			}
			digest, err := snapshotDigest(context.Background(), dir)
			if err != nil {
				t.Fatal(err)
			}
			Write(TurnPath(root, owner.Session), Turn{Owner: owner, Agent: agent, Repositories: []string{dir}})
			Write(StatePath(root, dir), State{Owner: owner, Fingerprint: digest, HypothesesSubmitted: true, RequestedAt: time.Now()})
			Write(filepath.Join(root, "heartbeat.json"), time.Now())
			url := "https://example.com/review#full-token"
			Write(FeedPath(root, dir), Feed{Owner: owner, Fingerprint: digest, ReviewID: "review", URL: url, AssessmentStatus: "ready", Updated: time.Now(), Leads: []Lead{{ID: "lead", Path: "file.txt", Line: 1, Concern: "Check the boundary condition", Check: "Exercise an empty result"}}})
			run := func(event, id string) map[string]any {
				raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: event, Tool: "Bash", ToolID: id, Input: json.RawMessage(`{"command":"pwd"}`)})
				var out strings.Builder
				hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), &out, agent)
				var result map[string]any
				if err := json.Unmarshal([]byte(out.String()), &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			stop := run("Stop", "")
			reason, _ := stop["reason"].(string)
			if stop["decision"] != "block" || !strings.Contains(reason, url) || !strings.Contains(reason, "1 area") {
				t.Fatal("missing public review summary", stop)
			}
			for _, private := range []string{"Investigate", "watch respond", "Record every", "final answer", "internal handoff"} {
				if strings.Contains(reason, private) {
					t.Fatal("private instructions leaked into Stop", reason)
				}
			}
			continuation, _ := json.Marshal(HookInput{Session: owner.Session, Turn: "continued-turn", CWD: dir, Event: "UserPromptSubmit", Prompt: reason})
			var continuationOutput strings.Builder
			hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(continuation)), &continuationOutput, agent)
			owner.Turn = "continued-turn"
			otherInput, _ := json.Marshal(HookInput{Session: "other-session", Turn: owner.Turn, CWD: dir, Event: "PreToolUse", Tool: "Bash", ToolID: "other-tool", Input: json.RawMessage(`{"command":"pwd"}`)})
			var otherOutput strings.Builder
			hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(otherInput)), &otherOutput, agent)
			if strings.Contains(otherOutput.String(), "Check the boundary condition") {
				t.Fatal("private review context crossed sessions")
			}
			malformed := run("PreToolUse", "")
			if strings.Contains(fmt.Sprint(malformed), "Check the boundary condition") {
				t.Fatal("malformed tool consumed private context")
			}
			tool := run("PreToolUse", "inspect")
			context, ok := tool["hookSpecificOutput"].(map[string]any)
			if !ok || context["hookEventName"] != "PreToolUse" || !strings.Contains(fmt.Sprint(context["additionalContext"]), "Check the boundary condition") || !strings.Contains(fmt.Sprint(context["additionalContext"]), "watch respond") {
				t.Fatal("private investigation details were not delivered", tool)
			}
			if tool["reason"] != nil || tool["systemMessage"] != nil {
				t.Fatal("private context used a public field", tool)
			}
			var saved Turn
			Read(TurnPath(root, owner.Session), &saved)
			if saved.ReviewContext != "" || len(saved.Pending) != 1 {
				t.Fatal("private context was not consumed or lost response tracking")
			}
			again := run("PreToolUse", "inspect-again")
			if strings.Contains(fmt.Sprint(again), "Check the boundary condition") {
				t.Fatal("private investigation was repeated", again)
			}
		})
	}
}

func TestReviewContextSurvivesStopContinuation(t *testing.T) {
	previous := Turn{
		Owner: Owner{"session", "old-turn"}, Prompt: "original task", Agent: "codex",
		ReviewContext: "private findings", ReviewContinuation: "public review summary",
		ResponseToken: "token", Next: 2, Pending: []Pending{{Number: 1, ReviewID: "review"}},
		Delivered: []string{"review"}, Repositories: []string{"/repo"},
	}
	for _, tc := range []struct {
		name, session, prompt string
		preserve              bool
	}{
		{"stop prompt", "session", "public review summary", true},
		{"tool continuation", "session", "", true},
		{"different task", "session", "a new unrelated task", false},
		{"different session", "other", "public review summary", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := Turn{Owner: Owner{tc.session, "new-turn"}, Prompt: tc.prompt, Agent: "codex", Next: 1}
			got := continueReviewContext(previous, next, tc.prompt)
			if got.Owner != next.Owner {
				t.Fatal("restored the previous owner")
			}
			if tc.preserve {
				if got.ReviewContext != previous.ReviewContext || got.ResponseToken != previous.ResponseToken || len(got.Pending) != 1 || got.Next != 2 || got.Prompt != previous.Prompt {
					t.Fatal("lost continuation context", got)
				}
			} else if got.ReviewContext != "" || len(got.Pending) != 0 || got.Prompt != next.Prompt {
				t.Fatal("injected old review into another task", got)
			}
		})
	}
}

func TestCompletedDeepReviewGuidanceStaysPrivate(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	owner := Owner{"session", "turn"}
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600)
	digest, err := snapshotDigest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner, Repositories: []string{dir}})
	Write(StatePath(root, dir), State{Owner: owner, Fingerprint: digest, RequestedAt: time.Now()})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	Write(FeedPath(root, dir), Feed{Owner: owner, Fingerprint: digest, ReviewID: "review", URL: "https://example.com/review", AssessmentStatus: "unavailable", FullReviewStatus: "completed", Updated: time.Now()})
	run := func(event, toolID string) string {
		raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: event, Tool: "Bash", ToolID: toolID, Input: json.RawMessage(`{"command":"pwd"}`)})
		var out strings.Builder
		hookForTest(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(raw)), &out)
		return out.String()
	}
	stop := run("Stop", "")
	if !strings.Contains(stop, "deeper review completed") || strings.Contains(stop, "Do not describe") {
		t.Fatal("incorrect public stage status", stop)
	}
	tool := run("PreToolUse", "inspect")
	if !strings.Contains(tool, "additionalContext") || !strings.Contains(tool, "Do not describe this as an overall review failure") {
		t.Fatal("lost private stage-status guidance", tool)
	}
}
