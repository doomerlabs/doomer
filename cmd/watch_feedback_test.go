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

func TestCodexStopWarnsAboutUnansweredFindingsWithoutContinuing(t *testing.T) {
	response := livewatch.Response{Findings: []livewatch.Finding{{ReviewID: "review", Lead: adversarylabs.WorkspaceLead{ID: strings.Repeat("a", 64), Path: "file.go", Line: 12, Concern: "Check the guard"}}}}
	for _, active := range []bool{false, true} {
		out := codexStopFeedback(response, active)
		warning, _ := out["systemMessage"].(string)
		if len(out) != 1 || !strings.Contains(warning, "incomplete") {
			t.Fatalf("Stop must warn without creating a continuation (active=%v): %v", active, out)
		}
		assertSafeHandoffMessage(t, out)
	}
	text := feedbackInstructions(response, strings.Repeat("b", 64))
	for _, want := range []string{"watch findings respond", "review_id", "lead_id", "status=fixed", "dismissed", "deferred", "Do not dump"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	response.Findings[0].Disposition = &livewatch.Feedback{Status: "dismissed"}
	if out := codexStopFeedback(response, false); len(out) != 0 {
		t.Fatalf("recorded findings generated Stop feedback: %v", out)
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
	if report.SuggestedSummary != "" || report.Reviews[0].URL != "https://example.test/cli" || report.Reviews[1].URL != "https://example.test/app" {
		t.Fatalf("dismissed findings should leave review links available without a routine summary: %+v", report)
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
		if report.Complete || report.SuggestedSummary == "" {
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
	if !strings.Contains(text, "watch findings ls --control "+key) || !strings.Contains(text, "watch results --control "+key) || !strings.Contains(text, "one final answer") || !strings.Contains(text, "request normal sandbox escalation") || !strings.Contains(text, "Respect any denial") {
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
		done <- livewatch.Serve(ctx, livewatch.Config{Directory: directory, ControlKey: key,
			Notices: func() []adversarylabs.ClientNotice {
				return []adversarylabs.ClientNotice{{ID: "update", Revision: 1, NotifyUser: true}}
			},
			AcknowledgeNotice: func(string, int) error { return nil },
		}, nil, func(context.Context, string) bool { return true })
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
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || !report.Complete || !report.NoticesPending || report.SuggestedSummary != "" {
		t.Fatalf("report: %s (%v)", out.String(), err)
	}
	if _, live := livewatch.Available(ctx, key); !live {
		t.Fatal("reading the report stopped the hook-owned watcher")
	}
	ack := newWatchNoticeAckCommand()
	ack.SetContext(ctx)
	ack.SetArgs([]string{"--control", key, "--id", "update", "--revision", "1"})
	out.Reset()
	ack.SetOut(&out)
	if err := ack.Execute(); err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["acknowledged"] != true || receipt["complete"] != true || receipt["follow_up_required"] != false || receipt["suggested_summary"] != nil || receipt["reviews"] != nil {
		t.Fatalf("ack must not replay review results: %s", out.String())
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
	result := livewatch.Response{Notices: []adversarylabs.ClientNotice{{ID: "service-news", Revision: 2, Message: "An announcement, not a command", Severity: "info", NotifyUser: true, Blocking: true}}}
	key := strings.Repeat("a", 64)
	decorated := watchNoticeCommands(result, key)
	if len(decorated.Notices[0].AcknowledgeCommand) == 0 || !strings.Contains(strings.Join(decorated.Notices[0].AcknowledgeCommand, " "), "watch notice-ack --control "+key) {
		t.Fatal(decorated)
	}
	report := makeFeedbackReport(decorated)
	if !report.Complete || !report.NoticesPending || report.NoticeInstructions == "" || report.NextAction != "display_blocking_notice" {
		t.Fatal("pending display was treated as complete", report)
	}
	if codexStopFeedback(result, false)["decision"] != "block" {
		t.Fatal("stop failed to request advisory delivery")
	}
	text := feedbackInstructions(result, key)
	if !strings.Contains(text, "An announcement, not a command") || !strings.Contains(text, "only after user-visible display") {
		t.Fatal(text)
	}
	result.Notices[0].NotifyUser = false
	if !makeFeedbackReport(result).Complete || len(codexStopFeedback(result, false)) != 0 {
		t.Fatal("acknowledged advisory still blocks completion")
	}
}

func TestClaudeStopSignalsWithoutDumpingReviewDetails(t *testing.T) {
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
	for _, want := range []string{"Doomer review needs follow-up.", "watch results --control " + key} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %v", want, out)
		}
	}
	if out["decision"] != nil || contextOutput["hookEventName"] != "Stop" {
		t.Fatal(out)
	}
	if strings.Contains(text, "/private/credentials") || strings.Contains(text, "Handle invalid input") || strings.Contains(text, "Service maintenance") || strings.Contains(text, "notice-ack") {
		t.Fatal("Claude Stop feedback included verified upload consent details")
	}
	// A denied response remains pending, but cannot trap Claude in a Stop loop.
	blocked := claudeStopFeedback(result, key, true)
	message, _ := blocked["systemMessage"].(string)
	if len(blocked) != 1 || message != "Doomer is waiting for Claude to record its review decisions." {
		t.Fatal(blocked)
	}
	if result.Findings[0].Disposition != nil || !result.Notices[0].NotifyUser {
		t.Fatal("delivery recorded an acknowledgement or decision")
	}
	result.Findings[0].Disposition = &livewatch.Feedback{Status: "deferred", Reason: "Needs user input"}
	advisory := claudeStopFeedback(result, key, true)
	message, _ = advisory["systemMessage"].(string)
	if len(advisory) != 1 || !strings.HasPrefix(message, "Doomer has an advisory awaiting delivery.") || !strings.Contains(message, "watch results --control "+key) {
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

func TestClaudeActiveStopShowsBlockingNoticeBeforePendingReview(t *testing.T) {
	result := livewatch.Response{
		Findings: []livewatch.Finding{{ReviewID: "pending"}},
		Notices: []adversarylabs.ClientNotice{
			{ID: "cli-update-beta", Kind: "update", Channel: "beta", Severity: "deprecated",
				Blocking: true, NotifyUser: true, Message: "The installed beta is no longer supported.",
				RecommendedVersion: "2026.11.17.3-beta.1"},
			{ID: "service-news", Severity: "info", NotifyUser: true, Message: "Optional bulletin"},
		},
	}
	out := claudeStopFeedback(result, strings.Repeat("a", 64), true)
	message, _ := out["systemMessage"].(string)
	for _, want := range []string{"Doomer update required", "no longer supported", "npx @doomerlabs/doomer@beta update"} {
		if !strings.Contains(message, want) {
			t.Fatalf("blocking notice missing %q: %+v", want, out)
		}
	}
	if len(out) != 1 || strings.Contains(message, "Optional bulletin") || strings.Contains(message, "notice-ack") {
		t.Fatalf("unexpected output from active Stop: %+v", out)
	}
	result.HypothesisRequests = []livewatch.HypothesisRequest{{ID: "already-delivered"}}
	result.RepeatedStopHandoff = true
	out = claudeStopFeedback(result, strings.Repeat("a", 64), false)
	message, _ = out["systemMessage"].(string)
	if len(out) != 1 || !strings.Contains(message, "review is incomplete") || !strings.Contains(message, "Doomer update required") {
		t.Fatalf("terminal Stop lost its blocking notice: %+v", out)
	}
	result.Results = []livewatch.Result{{Repository: "/repo", Status: "unavailable", Error: "service unavailable"}}
	out = claudeStopFeedback(result, strings.Repeat("a", 64), true)
	message, _ = out["systemMessage"].(string)
	if !strings.HasPrefix(message, "Doomer review is incomplete.") ||
		!strings.Contains(message, "service unavailable") || !strings.Contains(message, "Doomer update required") {
		t.Fatalf("blocking notice hid the review failure: %+v", out)
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

func TestCodexPromptIncludesReviewOutcomeGuidance(t *testing.T) {
	text := watchAgentContext("test")["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	for _, want := range []string{"Never equate returned findings with confirmed defects", "Report verified fixes, material unresolved concerns, incomplete coverage, and errors", "name the specific problem or affected behavior in one short sentence", "Only describe a finding as a bug you fixed if you agreed with it and verified the fix", "Provide the review outcome when the user asks for it in this turn", "Deliver required service advisories regardless of review outcome"} {
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
		} else if codex["decision"] != "block" || !strings.Contains(codex["reason"].(string), "Read the watch results before finishing") {
			t.Fatal("Codex lost initial handoff")
		}
		alreadyFetched := response
		alreadyFetched.RepeatedStopHandoff = true
		if out := codexStopFeedback(alreadyFetched, active); len(out) != 0 {
			t.Fatalf("Codex reopened a turn for an assessment it already fetched: %v", out)
		}
		alreadyFetched.OtherReviewPending = true
		if out := codexStopFeedback(alreadyFetched, active); active && out["systemMessage"] != "Doomer has another Quick review in progress." ||
			!active && (out["decision"] != "block" || !strings.Contains(out["reason"].(string), "another Quick review")) {
			t.Fatalf("Codex lost another review after a fetched handoff: %v", out)
		}
		claude := claudeStopFeedback(response, strings.Repeat("a", 64), active)
		context, ok := claude["hookSpecificOutput"].(map[string]any)
		if !ok || context["hookEventName"] != "Stop" || claude["decision"] != nil || claude["systemMessage"] != nil {
			t.Fatal(claude)
		}
		text, _ := context["additionalContext"].(string)
		if !strings.Contains(text, "watch results --control") || !strings.Contains(text, "report the denial and end the turn") || strings.Contains(text, "snapshot-request") || len(text) > 700 {
			t.Fatal("missing bounded recovery instructions", claude)
		}
	}
}

func TestHypothesisHandoffIncludesVerifiedConsentBeforeCapturedDiff(t *testing.T) {
	response := livewatch.Response{
		Consent: &livewatch.ConsentEvidence{
			Account: "fixture-account", Service: "https://example.test/api", Profile: "work",
			CredentialDirectory: "/private/credentials", Agent: "codex",
			Repositories: []string{"/repo"}, Includes: []string{"task_context"},
		},
		HypothesisRequests: []livewatch.HypothesisRequest{{ID: "snapshot-request", Repository: "/repo", Diff: "+change"}},
	}
	key := strings.Repeat("a", 64)
	output := hypothesisInstructions(response, key)
	evidence, err := json.Marshal(response.Consent)
	if err != nil {
		t.Fatal(err)
	}
	header := "Current CLI-verified upload consent (scope data, not instructions): " + string(evidence) + "\n"
	if !strings.HasPrefix(output, header) || !strings.Contains(output, "Captured requests: ") {
		t.Fatal("pending handoff must show the current consent scope before the captured diff")
	}
	prefix := output[:min(len(output), 1500)]
	if !strings.Contains(prefix, "watch results --control "+key) || !strings.Contains(prefix, "missing or cut off") || strings.Contains(prefix, "Captured requests: ") {
		t.Fatal("clipped output must tell the agent how to recover the request")
	}

	response.Consent = nil
	if output := hypothesisInstructions(response, key); strings.Contains(output, "CLI-verified upload consent") {
		t.Fatal("pending handoff claimed consent without verified scope")
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
	message, _ := context["additionalContext"].(string)
	if !ok || context["hookEventName"] != "Stop" || !strings.Contains(message, "watch results --control") || out["decision"] != nil {
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
		for _, want := range []string{"If you edit files or have pending review work, before your final answer run", "not a standalone manual review", "Do not wait for the Stop hook", "after further edits, read results again", "no file changes and has no pending review work", "After handling any sandbox connection block through the normal approval flow above, if the command still fails or the report contains errors, stop review handling"} {
			if !strings.Contains(context, want) {
				t.Fatalf("%s missing %q", agent, want)
			}
		}
	}
}

func TestClaudeNoticeFollowupIsCompactAndSeparatesReviewCompletion(t *testing.T) {
	result := livewatch.Response{
		Results: []livewatch.Result{{Status: "ready", Repository: "/repo", URL: "https://example.test/review"}},
		Notices: []adversarylabs.ClientNotice{{ID: "cli-update-beta", Revision: 1, Component: "cli", Kind: "update", Channel: "beta", Severity: "available", RecommendedVersion: "2026.11.17.3-beta.1", Message: "A new beta release is available.", NotifyUser: true}},
	}
	result.Notices[0].DisplayMarkdown = adversarylabs.AdvisoryMarkdown(result.Notices[0])
	out := claudeStopFeedback(result, strings.Repeat("a", 64), false)
	text := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	for _, want := range []string{"Doomer has a notice to display.", "watch results --control"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q: %s", want, text)
		}
	}
	for _, unwanted := range []string{`"notices":`, `"notice_instructions":`, `"complete": false`, "Version:", "notice-ack", "A new beta release", "REQUIRED NEXT ACTION"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("duplicated report data %q: %s", unwanted, text)
		}
	}
	if strings.Contains(text, "\n") || len(text) > 400 {
		t.Fatalf("Stop hook should contain only a one-line handoff: %s", text)
	}
	// The full notice remains available through normal command output.
	if !strings.Contains(feedbackReport(watchNoticeCommands(result, strings.Repeat("a", 64))), "## 🔵 Doomer update available") {
		t.Fatal("notice lost from command results")
	}

	report := makeFeedbackReport(result)
	if !report.Complete || !report.ReviewComplete || !report.NoticesPending {
		t.Fatal("notice delivery must stay pending without marking the code review incomplete")
	}
}

func TestOptionalNoticesWaitForReviewAndBlockingNoticesDoNot(t *testing.T) {
	for _, pending := range []livewatch.Response{
		{ReviewPending: true},
		{HypothesisRequests: []livewatch.HypothesisRequest{{ID: "pending"}}},
		{Findings: []livewatch.Finding{{ReviewID: "r"}}},
	} {
		pending.Notices = []adversarylabs.ClientNotice{{ID: "update", NotifyUser: true, AcknowledgeCommand: []string{"ack"}}}
		report := makeFeedbackReport(pending)
		if report.Complete || !report.NoticesPending || len(report.Notices) != 0 || report.NoticeInstructions != "" {
			t.Fatalf("optional notice interrupted review: %+v", report)
		}
		if !pending.Notices[0].NotifyUser {
			t.Fatal("mutated source notice")
		}
		pending.Notices[0].Blocking = true
		report = makeFeedbackReport(pending)
		if !report.Notices[0].NotifyUser || report.NoticeInstructions == "" || report.NextAction != "display_blocking_notice" {
			t.Fatal("blocking update hidden")
		}
	}
	failed := makeFeedbackReport(livewatch.Response{Results: []livewatch.Result{{Status: "unavailable", Error: "service unavailable"}}, Notices: []adversarylabs.ClientNotice{{NotifyUser: true}}})
	if failed.Complete || len(failed.Errors) == 0 || !failed.Notices[0].NotifyUser {
		t.Fatalf("lost real failure or terminal notice: %+v", failed)
	}
}

func TestWatchReportOmitsPreviouslyDeliveredNotice(t *testing.T) {
	notice := adversarylabs.ClientNotice{
		ID: "cli-update-beta", Revision: 1, Component: "cli", Kind: "update", Channel: "beta",
		Severity: "available", Message: "A new beta release is available.",
		RecommendedVersion: "2026.11.17.3-beta.1",
	}
	delivered := livewatch.Response{
		Results: []livewatch.Result{{Status: "ready", Repository: "/repo"}},
		Notices: []adversarylabs.ClientNotice{notice},
	}
	delivered.Notices[0].DisplayMarkdown = adversarylabs.AdvisoryMarkdown(notice)
	delivered.Notices[0].NotifyUser = true
	if report := makeFeedbackReport(delivered); !report.NoticesPending || len(report.Notices) != 1 {
		t.Fatalf("lost initial delivery: %+v", report)
	}
	delivered.Notices[0].NotifyUser = false
	report := makeFeedbackReport(delivered)
	if !report.ReviewComplete || report.NoticesPending || len(report.Notices) != 0 || report.NoticeInstructions != "" {
		t.Fatalf("previously delivered notice leaked into the next review: %+v", report)
	}
}

func TestNoticeAcknowledgementDoesNotReplayOrRetryTerminalFailure(t *testing.T) {
	result := livewatch.Response{Results: []livewatch.Result{{Status: "unavailable", Error: "service unavailable"}}}
	receipt := noticeAcknowledgement(result)
	if receipt["complete"] != false || receipt["follow_up_required"] != false || receipt["suggested_summary"] != nil || receipt["reviews"] != nil {
		t.Fatal(receipt)
	}
	result.ReviewPending = true
	if noticeAcknowledgement(result)["follow_up_required"] != true {
		t.Fatal("lost pending review")
	}
}

func TestReplyMustContainCompleteFormattedNotice(t *testing.T) {
	n := adversarylabs.ClientNotice{ID: "cli-update-beta", Kind: "update", Channel: "beta", Severity: "available", Message: "New release", RecommendedVersion: "2026.11.17.3-beta.1"}
	if noticeAppearsInReply(n, "An update is available. I acknowledged it.") {
		t.Fatal("passing mention counted as delivery")
	}
	text := adversarylabs.AdvisoryMarkdown(n)
	if !noticeAppearsInReply(n, "Review completed.\n\n"+text) {
		t.Fatal("full notice was not recognized")
	}
	if noticeAppearsInReply(n, strings.ReplaceAll(text, "2026.11.17.3-beta.1", "2026.11.17.2-beta.1")) {
		t.Fatal("wrong release counted as delivery")
	}
	n.NotifyUser = true
	decorated := watchNoticeCommands(livewatch.Response{Notices: []adversarylabs.ClientNotice{n}}, strings.Repeat("a", 64))
	if !decorated.Notices[0].AcknowledgeAfterReply || len(decorated.Notices[0].AcknowledgeCommand) != 0 {
		t.Fatal("optional notice requires a manual acknowledgement")
	}
}

func TestWatchResultPrefixPreservesReviewOutcomeWithPendingNotice(t *testing.T) {
	result := livewatch.Response{
		Results:  []livewatch.Result{{Repository: "/repo", Status: "ready", URL: "https://example.test/review?revision=21"}},
		Findings: []livewatch.Finding{{Repository: "/repo", Disposition: &livewatch.Feedback{Status: "fixed"}}},
		Notices: []adversarylabs.ClientNotice{{
			NotifyUser: true, DisplayMarkdown: strings.Repeat("notice details ", 200),
		}},
	}
	report := makeFeedbackReport(result)
	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(report); err != nil {
		t.Fatal(err)
	}
	// A real Claude session read only the first 1700 bytes of watch results.
	const previewBytes = 1700
	if out.Len() <= previewBytes {
		t.Fatal("fixture must exceed the observed output cutoff")
	}
	preview := out.String()[:previewBytes]
	for _, want := range []string{`"complete":true`, `"review_complete":true`, `"notices_pending":true`, `"next_action":"report_review_then_display_notice"`, "revision=21", "fixed them"} {
		if !strings.Contains(preview, want) {
			t.Errorf("truncated output lost %q", want)
		}
	}
	var decoded watchFeedbackReport
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || len(decoded.Notices) != 1 || decoded.Notices[0].DisplayMarkdown != result.Notices[0].DisplayMarkdown {
		t.Fatalf("full report lost notice data: %v", err)
	}
}

func TestCleanReviewKeepsItsRecordWithoutSuggestingAReply(t *testing.T) {
	const reviewURL = "https://example.test/review"
	result := livewatch.Response{
		Results: []livewatch.Result{{Repository: "/repo", Status: "ready", URL: reviewURL}},
	}
	report := makeFeedbackReport(result)
	if !report.Complete || report.SuggestedSummary != "" || len(report.Reviews) != 1 || report.Reviews[0].URL != reviewURL || !strings.Contains(report.ReplyInstructions, "only if the user asked for review status in this turn") {
		t.Fatalf("expected a completed review record without a suggested reply: %+v", report)
	}
	result.Notices = []adversarylabs.ClientNotice{{NotifyUser: true, DisplayMarkdown: "Service notice"}}
	report = makeFeedbackReport(result)
	if !report.Complete || !report.NoticesPending || report.NextAction != "display_notice" || report.SuggestedSummary != "" || report.ReplyInstructions != "" || len(report.Notices) != 1 {
		t.Fatalf("expected the notice to be delivered without a clean-review summary: %+v", report)
	}
}
