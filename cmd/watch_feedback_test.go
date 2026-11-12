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
	if visible != "Doomer has 1 suggestion ready for review." {
		t.Fatal(visible)
	}
	for _, hidden := range []string{"watch instructions", "--control", "review_id", "lead_id", "status=fixed", "Do not dump", "internal handoff"} {
		if strings.Contains(visible, hidden) {
			t.Fatalf("verbose guidance leaked into hook reason: %s", visible)
		}
	}
	text := feedbackInstructions(response, strings.Repeat("b", 64))
	for _, want := range []string{"watch respond", "review_id", "lead_id", "status=fixed", "dismissed", "deferred", "Do not dump"} {
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
	want := "Doomer reviewed the [changes in doomer](https://example.test/cli) and [changes in doomerlabs](https://example.test/app); we considered its suggestions and made no changes based on them."
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
	if !strings.Contains(text, "watch instructions --control "+key) || !strings.Contains(text, "watch report --control "+key) || !strings.Contains(text, "ONE final answer") {
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
	command := newWatchReportCommand()
	command.SetContext(ctx)
	command.SetArgs([]string{"--control", key})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var report watchFeedbackReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || !report.Complete || report.SuggestedSummary != "Doomer found no changes to review." {
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
