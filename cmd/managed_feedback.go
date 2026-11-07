package cmd

import (
	"encoding/json"
	"fmt"
)

// Hook feedback is visible in the coding chat. Structured review details stay
// available through task status rather than being copied into every hook.
func managedHookFeedback(event string, output []byte, operationErr error) map[string]any {
	var result managedResult
	message := "Doomer could not check the review. Resume the prepared task and inspect its status."
	valid := operationErr == nil && json.Unmarshal(output, &result) == nil && result.State != ""
	if valid {
		if result.State == "complete" || result.State == "paused" {
			return map[string]any{}
		}
		remaining := 0
		if result.Review != nil && result.Review.Early != nil {
			for _, lead := range result.Review.Early.Leads {
				if !result.Responses[result.Review.ID+":"+lead.ID].Sent {
					remaining++
				}
			}
		}
		if result.State == "no_changes" && remaining == 0 && event != "Stop" {
			return map[string]any{}
		}
		switch {
		case remaining > 0:
			message = fmt.Sprintf("Doomer needs responses to its review findings (%d pending). Read the prepared task's status and consent evidence, then record the responses before finishing.", remaining)
		case result.State == "no_changes":
			message = "No changes need review. Run the prepared task’s finish command to complete it."
		case result.State == "review_pending":
			message = "Doomer review is still running. Wait for the prepared task's result before finishing."
		case result.State == "authentication_required" || result.State == "consent_required" || result.State == "approval_required":
			message = "Doomer needs repository authorization. Resume the prepared task for the required action."
		case result.State == "operation_failed" || result.State == "review_failed":
			message = "Doomer could not complete the review. Read the prepared task's status for details and recovery."
		default:
			message = "Doomer review is not complete. Resume the prepared task before finishing."
		}
		if event != "Stop" && (result.State == "ready" || result.State == "review_pending" || result.State == "reviewed") && remaining == 0 {
			return map[string]any{}
		}
	}
	if event == "Stop" && valid && (result.State == "review_failed" || result.State == "operation_failed" && result.Review == nil) {
		return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": "Doomer review remains incomplete after a failure. Report the failure and end this turn. Resume the same task explicitly after the cause is addressed."}}
	}
	if event == "Stop" {
		return map[string]any{"decision": "block", "reason": message}
	}
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": message}}
}
