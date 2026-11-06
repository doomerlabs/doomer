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
	result := managedResult{State: "operation_failed", Task: "internal-task-handle", Error: "record every finding"}
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
		if !strings.Contains(string(feedback), "review findings (1 pending)") {
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
