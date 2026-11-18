package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/livewatch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestFeedbackHookRequestsEveryUnansweredFinding(t *testing.T) {
	response := livewatch.Response{Findings: []livewatch.Finding{{ReviewID: "review", Lead: adversarylabs.WorkspaceLead{ID: strings.Repeat("a", 64), Path: "file.go", Line: 12, Concern: "Check the guard"}}}}
	out := pendingFeedbackHook(response)
	if out["decision"] != "block" {
		t.Fatal(out)
	}
	visible := out["reason"].(string)
	if visible != "Doomer review is ready." {
		t.Fatal(visible)
	}
	for _, hidden := range []string{"watch findings", "--control", "review_id", "lead_id", "status=fixed", "Do not dump", "internal handoff"} {
		if strings.Contains(visible, hidden) {
			t.Fatalf("verbose guidance leaked into hook reason: %s", visible)
		}
	}
	text := feedbackInstructions(response, strings.Repeat("b", 64))
	for _, want := range []string{"watch findings respond", "review_id", "lead_id", "status=fixed", "dismissed", "deferred", "Do not dump"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	response.Findings[0].Disposition = &livewatch.Feedback{Status: "dismissed"}
	if pendingFeedbackHook(response) != nil {
		t.Fatal("asked for recorded response again")
	}
}

func TestFinalFeedbackHookStaysSilentAfterRecordedDecisions(t *testing.T) {
	response := livewatch.Response{}
	for i, status := range []string{"fixed", "fixed", "dismissed"} {
		response.Findings = append(response.Findings, livewatch.Finding{ReviewID: "review", Repository: "/repo", URL: "https://doomer.ai/review/example", Lead: adversarylabs.WorkspaceLead{Path: "file.go", Line: i + 1, Concern: "guard"}, Disposition: &livewatch.Feedback{Status: status, Reason: "Verified change"}})
	}
	if out := finalFeedbackHook(response); len(out) != 0 {
		t.Fatalf("completed review requested another answer: %v", out)
	}
	report := makeFeedbackReport(response)
	if !report.Complete || report.Suggestions != 3 || report.Fixed != 2 || report.Disagreed != 1 || len(report.Fixes) != 2 || report.Fixes[0].Reason != "Verified change" {
		t.Fatalf("missing recorded outcomes: %+v", report)
	}
	response.Findings[2].Disposition.Status = "deferred"
	report = makeFeedbackReport(response)
	if report.Deferred != 1 || len(report.DeferredFindings) != 1 || !strings.Contains(report.SuggestedSummary, "remain deferred") {
		t.Fatalf("deferred concern hidden: %+v", report)
	}
}

func TestFeedbackReportUsesLatestNaturalLinksPerRepository(t *testing.T) {
	response := livewatch.Response{
		Findings: []livewatch.Finding{{Repository: "/workspace/doomer", URL: "https://example.test/old", Disposition: &livewatch.Feedback{Status: "dismissed"}}},
		Results: []livewatch.Result{
			{Repository: "/workspace/doomer", URL: "https://example.test/cli", Status: "ready"},
			{Repository: "/workspace/doomerlabs", URL: "https://example.test/app", Status: "ready"},
		},
	}
	report := makeFeedbackReport(response)
	if !report.Complete || len(report.Reviews) != 2 {
		t.Fatal(report)
	}
	want := "Doomer reviewed the [changes in doomer](https://example.test/cli) and [changes in doomerlabs](https://example.test/app); I checked its concerns and no further changes were needed."
	if report.SuggestedSummary != want {
		t.Fatal(report.SuggestedSummary)
	}
	if strings.Contains(feedbackReport(response), "https://example.test/old") {
		t.Fatal("historical review duplicated the current repository link")
	}
}

func TestPartialFeedbackReportDoesNotClaimCompletion(t *testing.T) {
	for _, response := range []livewatch.Response{
		{Findings: []livewatch.Finding{{Lead: adversarylabs.WorkspaceLead{ID: "pending"}}}},
		{Results: []livewatch.Result{{Status: "unavailable", Error: "upload failed"}}},
	} {
		report := makeFeedbackReport(response)
		if report.Complete || strings.Contains(report.SuggestedSummary, "raised no issues") {
			t.Fatal(report)
		}
	}
	if len(finalFeedbackHook(livewatch.Response{Results: []livewatch.Result{{Status: "unavailable"}}})) == 0 {
		t.Fatal("incomplete review was suppressed")
	}
}

func TestWatchCommandsGoOnlyToPromptContext(t *testing.T) {
	key := strings.Repeat("b", 64)
	output := watchAgentContext(key)
	if output["reason"] != nil || output["systemMessage"] != nil {
		t.Fatal("prompt instructions placed in visible output")
	}
	specific := output["hookSpecificOutput"].(map[string]any)
	if specific["hookEventName"] != "UserPromptSubmit" {
		t.Fatal(specific)
	}
	text := specific["additionalContext"].(string)
	if !strings.Contains(text, "watch findings ls --control "+key) || !strings.Contains(text, "watch results --control "+key) || !strings.Contains(text, "ONE final answer") || !strings.Contains(text, "request normal sandbox escalation") || !strings.Contains(text, "Respect any denial") {
		t.Fatal("missing command in model context")
	}
}

func TestWatchReportLeavesShutdownToHook(t *testing.T) {
	directory := t.TempDir()
	hash := sha256.Sum256([]byte(directory))
	key := hex.EncodeToString(hash[:])
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- livewatch.Serve(ctx, livewatch.Config{Directory: directory, ControlKey: key}, nil, func(context.Context, string) bool { return true })
	}()
	for {
		if _, live := livewatch.Available(ctx, key); live {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("watch startup: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	command := newWatchResultsCommand()
	command.SetContext(ctx)
	command.SetArgs([]string{"--control", key})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var report watchFeedbackReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || !report.Complete || report.SuggestedSummary != "" {
		t.Fatalf("report: %s (%v)", out.String(), err)
	}
	if _, live := livewatch.Available(ctx, key); !live {
		t.Fatal("reading the report stopped the hook-owned watcher")
	}
	result, err := livewatch.Call(ctx, key, "finish")
	if err != nil || len(finalFeedbackHook(result)) != 0 {
		t.Fatalf("Stop should silently close a completed review: %+v %v", result, err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Stop did not stop the worker")
	}
	if _, live := livewatch.Available(ctx, key); live {
		t.Fatal("completed worker remains available to repeat the review")
	}
}

func TestAdvisoryRequiresExplicitDisplayAcknowledgement(t *testing.T) {
	result := livewatch.Response{Notices: []adversarylabs.ClientNotice{{ID: "service-news", Revision: 2, Message: "An announcement, not a command", Severity: "info", NotifyUser: true}}}
	key := strings.Repeat("a", 64)
	decorated := watchNoticeCommands(result, key)
	if len(decorated.Notices[0].AcknowledgeCommand) == 0 || !strings.Contains(strings.Join(decorated.Notices[0].AcknowledgeCommand, " "), "watch notice-ack --control "+key) {
		t.Fatal(decorated)
	}
	report := makeFeedbackReport(decorated)
	if report.Complete || report.NoticeInstructions == "" {
		t.Fatal("pending display was treated as complete", report)
	}
	if pendingFeedbackHook(result) == nil {
		t.Fatal("stop failed to request advisory delivery")
	}
	text := feedbackInstructions(result, key)
	if !strings.Contains(text, "An announcement, not a command") || !strings.Contains(text, "only after user-visible display") {
		t.Fatal(text)
	}
	result.Notices[0].NotifyUser = false
	if !makeFeedbackReport(result).Complete || pendingFeedbackHook(result) != nil {
		t.Fatal("acknowledged advisory still blocks completion")
	}
}

func TestClaudeStopDeliversReviewWithoutShellRetrieval(t *testing.T) {
	key := strings.Repeat("a", 64)
	context := watchAgentContextForAgent(key, "claude")["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(context, "before your final answer run") || !strings.Contains(context, "watch results --control "+key) || !strings.Contains(context, "watch findings ls --control "+key) || !strings.Contains(context, "Do not wait for the Stop hook") || !strings.Contains(context, "literal executable path") {
		t.Fatal("Claude is missing internal handoff retrieval guidance")
	}
	result := livewatch.Response{
		Consent:  &livewatch.ConsentEvidence{CredentialDirectory: "/private/credentials"},
		Results:  []livewatch.Result{{Status: "ready", URL: "https://example.test/review"}},
		Findings: []livewatch.Finding{{ReviewID: "review", Lead: adversarylabs.WorkspaceLead{ID: "lead", Path: "hello.py", Line: 1, Concern: "Handle invalid input"}}},
		Notices:  []adversarylabs.ClientNotice{{ID: "news", Revision: 2, Message: "Service maintenance", NotifyUser: true}},
	}
	out := claudeStopFeedback(result, key, false)
	contextOutput, _ := out["hookSpecificOutput"].(map[string]any)
	text, _ := contextOutput["additionalContext"].(string)
	for _, want := range []string{"Handle invalid input", "Service maintenance", "watch findings respond", "notice-ack", "only after user-visible display", "do not bypass"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %v", want, out)
		}
	}
	if out["decision"] != nil || contextOutput["hookEventName"] != "Stop" {
		t.Fatal(out)
	}
	if strings.Contains(text, "/private/credentials") {
		t.Fatal("Claude Stop feedback included verified upload consent details")
	}
	// A denied response remains pending, but cannot trap Claude in a Stop loop.
	blocked := claudeStopFeedback(result, key, true)
	message, _ := blocked["systemMessage"].(string)
	if len(blocked) != 1 || !strings.HasPrefix(message, "Doomer is waiting for Claude to record its review decisions.") ||
		!strings.Contains(message, "Service maintenance") || !strings.Contains(message, `"notice-ack","--control","`+key+`"`) ||
		strings.Contains(message, "/private/credentials") || strings.Contains(message, "watch findings respond") {
		t.Fatal(blocked)
	}
	if result.Findings[0].Disposition != nil || !result.Notices[0].NotifyUser {
		t.Fatal("delivery recorded an acknowledgement or decision")
	}
	result.Findings[0].Disposition = &livewatch.Feedback{Status: "deferred", Reason: "Needs user input"}
	advisory := claudeStopFeedback(result, key, true)
	message, _ = advisory["systemMessage"].(string)
	if len(advisory) != 1 || !strings.Contains(message, "Service maintenance") || !strings.Contains(message, `"notice-ack","--control","`+key+`"`) {
		t.Fatal(advisory)
	}
	result.Findings[0].Disposition = nil
	result.Notices[0].NotifyUser = false
	if out := claudeStopFeedback(result, key, true); len(out) != 1 || out["systemMessage"] != "Doomer is waiting for Claude to record its review decisions." {
		t.Fatal(out)
	}
	result.Findings[0].Disposition = &livewatch.Feedback{Status: "deferred", Reason: "Needs user input"}
	if out := claudeStopFeedback(result, key, true); len(out) != 0 {
		t.Fatal(out)
	}
}

func TestClaudeFailedReviewsStayIncompleteWithoutStopLoop(t *testing.T) {
	cases := map[string]livewatch.Response{
		"service outage":      {Results: []livewatch.Result{{Repository: "/repo", Status: "unavailable", Error: "service unavailable"}}},
		"interrupted request": {Results: []livewatch.Result{{Repository: "/repo", Status: "unavailable", Error: "review did not finish before watch stopped"}}},
		"transport failure":   {Error: "connection lost"},
	}
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			report := makeFeedbackReport(result)
			if report.Complete || len(report.Errors) == 0 {
				t.Fatal("failure reported as completed", report)
			}
			first := claudeStopFeedback(result, strings.Repeat("a", 64), false)
			text, _ := first["reason"].(string)
			if first["decision"] != "block" || !strings.Contains(text, report.Errors[0]) {
				t.Fatal("first delivery lost failure", first)
			}
			for i := 0; i < 3; i++ {
				repeat := claudeStopFeedback(result, strings.Repeat("a", 64), true)
				if repeat["decision"] != nil || !strings.Contains(repeat["systemMessage"].(string), "incomplete") {
					t.Fatal("failure trapped the agent or claimed completion", repeat)
				}
			}
		})
	}
}

func TestPendingFeedbackMessageDoesNotExposeFindingCounts(t *testing.T) {
	response := livewatch.Response{}
	for i := 0; i < 11; i++ {
		response.Findings = append(response.Findings, livewatch.Finding{})
	}
	if got := pendingFeedbackHook(response)["reason"]; got != "Doomer review is ready." {
		t.Fatalf("finding count leaked: %v", got)
	}
	text := watchAgentContext("test")["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	for _, want := range []string{"not text to repeat", "Never equate returned findings with confirmed defects", "Report the assessed outcome once", "one short sentence naming the specific problem", "Only describe a finding as a bug you fixed if you agreed with it and verified the fix", "do not merely say that Doomer found a problem", "omit all review commentary", "Still deliver required service advisories and report errors"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing outcome guidance: %s", want)
		}
	}
}

func TestPendingHypothesesKeepStopOpenAndRequestAgentAssessment(t *testing.T) {
	response := livewatch.Response{HypothesisRequests: []livewatch.HypothesisRequest{{ID: "snapshot-request", Repository: "/repo", Diff: "+change"}}}
	if makeFeedbackReport(response).Complete {
		t.Fatal("missing agent assessment reported complete")
	}
	text := hypothesisInstructions(response, strings.Repeat("a", 64))
	for _, want := range []string{"watch hypotheses --control", "explicit empty", "snapshot-request", "do not narrate"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s", want)
		}
	}
	for _, active := range []bool{false, true} {
		codex := codexStopFeedback(response, active)
		if active {
			if codex["decision"] != nil || codex["systemMessage"] == nil {
				t.Fatal("Codex retried an incomplete handoff indefinitely")
			}
			assertSafeHandoffMessage(t, codex)
		} else if codex["decision"] != "block" || codex["reason"] != "Doomer is checking the latest changes." {
			t.Fatal("Codex lost initial handoff")
		}
		claude := claudeStopFeedback(response, strings.Repeat("a", 64), active)
		context, ok := claude["hookSpecificOutput"].(map[string]any)
		if !ok || context["hookEventName"] != "Stop" || claude["decision"] != nil || claude["systemMessage"] != nil {
			t.Fatal(claude)
		}
		text, _ := context["additionalContext"].(string)
		if active && (!strings.Contains(text, "watch results --control") || strings.Contains(text, "snapshot-request") || len(text) > 600) {
			t.Fatal("replacement snapshot omitted", claude)
		}
		if !active && text != "Doomer is checking the latest changes." {
			t.Fatal(claude)
		}
	}
}

func assertSafeHandoffMessage(t *testing.T, output map[string]any) {
	t.Helper()
	message, _ := output["systemMessage"].(string)
	for _, private := range []string{"watch hypotheses", "--control", strings.Repeat("a", 64)} {
		if strings.Contains(message, private) {
			t.Fatalf("internal handoff leaked: %s", private)
		}
	}
}

func TestClaudePendingAssessmentUsesNonErrorStopFeedback(t *testing.T) {
	out := claudeStopFeedback(livewatch.Response{HypothesisRequests: []livewatch.HypothesisRequest{{ID: "pending"}}}, strings.Repeat("a", 64), false)
	context, ok := out["hookSpecificOutput"].(map[string]any)
	if !ok || context["hookEventName"] != "Stop" || context["additionalContext"] != "Doomer is checking the latest changes." || out["decision"] != nil {
		t.Fatal(out)
	}
}

func TestUnchangedTurnIsSilentForBothAgents(t *testing.T) {
	result := livewatch.Response{Results: []livewatch.Result{{Status: "unchanged"}}}
	for _, out := range []map[string]any{claudeStopFeedback(result, strings.Repeat("a", 64), false), codexStopFeedback(result, false)} {
		if len(out) != 0 {
			t.Fatalf("unchanged turn emitted feedback: %v", out)
		}
	}
	text := watchAgentContext(strings.Repeat("a", 64))["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if strings.Contains(text, "Before your final answer, run") || !strings.Contains(text, "do not call watch results") {
		t.Fatal("unconditional retrieval instruction remains")
	}
}

func TestCompletedPreAnswerReviewIsSilentForBothAgents(t *testing.T) {
	for _, result := range []livewatch.Response{
		{Results: []livewatch.Result{{Status: "ready", URL: "https://example.test/review"}}},
		{Results: []livewatch.Result{{Status: "ready"}}, Findings: []livewatch.Finding{{Disposition: &livewatch.Feedback{Status: "dismissed"}}}},
		{Results: []livewatch.Result{{Status: "ready"}}, Notices: []adversarylabs.ClientNotice{{ID: "delivered", NotifyUser: false}}},
	} {
		for _, active := range []bool{false, true} {
			for _, output := range []map[string]any{claudeStopFeedback(result, strings.Repeat("a", 64), active), codexStopFeedback(result, active)} {
				if len(output) != 0 {
					t.Fatalf("completed review emitted another visible message: %v", output)
				}
			}
		}
	}
}

func TestBothAgentsCompleteEditsBeforeFinalAnswer(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		context := watchAgentContextForAgent(strings.Repeat("a", 64), agent)["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
		for _, want := range []string{"If you edit files, before your final answer run", "not a standalone manual review", "Do not wait for the Stop hook", "after further edits, read results again", "no file changes and has no pending review work"} {
			if !strings.Contains(context, want) {
				t.Fatalf("%s missing %q", agent, want)
			}
		}
	}
}
