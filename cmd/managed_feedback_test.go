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

func TestBlockedAuthorizationDoesNotGateLocalCompletion(t *testing.T) {
	for _, state := range []string{"authentication_required", "consent_required", "approval_required"} {
		raw, _ := json.Marshal(managedResult{State: state})
		if got := managedHookFeedback("Stop", raw, nil); len(got) != 0 {
			t.Fatal(state, got)
		}
		if got := managedHookFeedback("PostToolUse", raw, nil); len(got) == 0 {
			t.Fatal("missing blocker diagnostic", state)
		}
	}
}

func TestManagedNoticeDeliveryAndNoStopLoop(t *testing.T) {
	result := managedResult{State: "complete", Notices: []adversarylabs.ClientNotice{{ID: "fix", Severity: "critical", Message: "Update needed", RecommendedVersion: "2026.11.9.1", NotifyUser: true}}}
	raw, _ := json.Marshal(result)
	feedback, _ := json.Marshal(managedHookFeedback("PostToolUse", raw, nil))
	if !strings.Contains(string(feedback), "Update needed") {
		t.Fatal(string(feedback))
	}
	result.State = "client_update_required"
	raw, _ = json.Marshal(result)
	if got := managedHookFeedback("Stop", raw, nil); len(got) != 0 {
		t.Fatal("blocked update restarted stop hook", got)
	}
	result.State = "complete"
	result.Notices[0].NotifyUser = false
	raw, _ = json.Marshal(result)
	if got := managedHookFeedback("PostToolUse", raw, nil); len(got) != 0 {
		t.Fatal("deduplicated notice shown again", got)
	}
}

func TestUpdateRequiredDoesNotBecomeNetworkHelp(t *testing.T) {
	result := managedResult{State: "client_update_required", Action: "Use the verified updater"}
	command := NewRootCommand()
	for _, status := range []int{403, 409, 426} {
		err := managed.NetworkError("review_submission", &adversarylabs.ResponseError{StatusCode: status, Code: "client_update_required", Host: "doomer.ai"})
		addManagedNetworkHelp(command, &result, managed.Task{}, err)
		if result.Action != "Use the verified updater" || result.NetworkAccess != nil {
			t.Fatal("update action was overwritten", result)
		}
	}
}
