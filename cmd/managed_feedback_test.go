package cmd

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestManagedHookFeedbackDoesNotExposeReviewPayload(t *testing.T) {
	result := managedResult{State: "operation_failed", NoChanges: true, Task: "internal-task-handle", Error: "record every finding"}
	if err := json.Unmarshal([]byte(`{"id":"review-id","early_assessment":{"status":"ready","leads":[{"id":"lead-id","path":"private/path.css","concern":"private assessment","check":"private check"}]}}`), &result.Review); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	for _, event := range []string{"Stop", "PostToolUse"} {
		feedback, _ := json.Marshal(managedHookFeedback(event, raw, nil))
		for _, secret := range []string{"internal-task-handle", "review-id", "private/path.css", "private assessment", "early_assessment", "operation_failed"} {
			if strings.Contains(string(feedback), secret) {
				t.Fatalf("exposed %s: %s", secret, feedback)
			}
		}
		if event != "Stop" && !strings.Contains(string(feedback), "review findings (1 pending)") {
			t.Fatal(string(feedback))
		}
	}
}

func TestManagedHookFeedbackKeepsCompletionGateAndSilencesRoutineEvents(t *testing.T) {
	for _, state := range []string{"ready", "review_pending", "reviewed", "complete", "paused"} {
		raw, _ := json.Marshal(managedResult{State: state})
		if got := managedHookFeedback("PostToolUse", raw, nil); len(got) != 0 {
			t.Fatal(state, got)
		}
		got := managedHookFeedback("Stop", raw, nil)
		if state == "complete" || state == "paused" {
			if len(got) != 0 {
				t.Fatal(state, got)
			}
		} else if got["decision"] != "block" {
			t.Fatal(state, got)
		}
	}
	for _, raw := range [][]byte{[]byte("not JSON private-error"), []byte(`{}`)} {
		feedback, _ := json.Marshal(managedHookFeedback("Stop", raw, errors.New("private-error")))
		if strings.Contains(string(feedback), "private-error") {
			t.Fatal(string(feedback))
		}
	}
}

func TestManagedHookFeedbackCountsOnlyUnsentFindingResponses(t *testing.T) {
	var review *adversarylabs.WorkspaceReview
	if err := json.Unmarshal([]byte(`{"id":"review-id","early_assessment":{"status":"ready","leads":[{"id":"lead-id"}]}}`), &review); err != nil {
		t.Fatal(err)
	}
	result := managedResult{State: "reviewed", Review: review, Responses: map[string]managed.Response{"review-id:lead-id": {Sent: true}}}
	raw, _ := json.Marshal(result)
	if got := managedHookFeedback("PostToolUse", raw, nil); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestNoChangesFeedbackKeepsErrorsVisible(t *testing.T) {
	for _, state := range []string{"authentication_required", "operation_failed", "review_failed"} {
		raw, _ := json.Marshal(managedResult{State: state, NoChanges: true})
		if got := managedHookFeedback("PostToolUse", raw, nil); len(got) == 0 {
			t.Fatal("hidden state", state)
		}
	}
	raw, _ := json.Marshal(managedResult{State: "no_changes", NoChanges: true})
	if got := managedHookFeedback("PostToolUse", raw, nil); len(got) != 0 {
		t.Fatal(got)
	}
	got := managedHookFeedback("Stop", raw, nil)
	if got["decision"] != "block" || !strings.Contains(got["reason"].(string), "finish command") {
		t.Fatal(got)
	}
}

func TestPendingFindingsStillBlockStopWithoutExposingPayload(t *testing.T) {
	result := managedResult{State: "reviewed"}
	if err := json.Unmarshal([]byte(`{"id":"review","early_assessment":{"status":"ready","leads":[{"id":"lead","path":"private-path","concern":"private concern"}]}}`), &result.Review); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	feedback := managedHookFeedback("Stop", raw, nil)
	if feedback["decision"] != "block" {
		t.Fatal(feedback)
	}
	body, _ := json.Marshal(feedback)
	if !strings.Contains(string(body), "1 pending") || strings.Contains(string(body), "private") {
		t.Fatal(string(body))
	}
}
