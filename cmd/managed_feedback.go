package cmd

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Hook feedback is visible in the coding chat. Structured review details stay
// available through task status rather than being copied into every hook.
func managedHookFeedback(event string, output []byte, operationErr error) map[string]any {
	var result managedResult
	message := "Doomer could not check the review. Resume the prepared task and inspect its status."
	noticeMessage := ""
	valid := operationErr == nil && json.Unmarshal(output, &result) == nil && result.State != ""
	if valid {
		var messages []string
		for _, n := range result.Notices {
			if n.NotifyUser {
				messages = append(messages, "Doomer "+n.Severity+": "+n.Message+" Recommended version: "+n.RecommendedVersion+". Use the existing verified updater and preserve update permissions.")
			}
		}
		noticeMessage = strings.Join(messages, "\n")
	}
	quiet := func() map[string]any {
		if event != "Stop" && noticeMessage != "" {
			return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": noticeMessage}}
		}
		return map[string]any{}
	}
	if event == "Stop" && valid && (result.State == "client_update_required" || result.State == "operation_failed" || result.State == "review_failed" || result.State == "authentication_required" || result.State == "consent_required" || result.State == "approval_required") {
		// Even additionalContext on Stop can restart Claude. The failed operation
		// already returned its diagnostic; let the turn end without more feedback.
		// Missing authorization also leaves local work available; the skill honors
		// any explicit user requirement to wait for review.
		return quiet()
	}
	if valid {
		if result.State == "complete" || result.State == "paused" {
			return quiet()
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
			return quiet()
		}
		switch {
		case remaining > 0:
			message = fmt.Sprintf("Doomer needs responses to its Quick review findings (%d pending). Read the prepared task's status and consent evidence, then record the responses before finishing.", remaining)
		case result.State == "reviewed":
			message = "Doomer Quick review is ready. Run the prepared task's finish command to verify responses and current source. Do not fetch Deep results or wait for Deep unless explicitly requested."
		case result.State == "no_changes":
			message = "No changes need review. Run the prepared task’s finish command to complete it."
		case result.State == "review_pending":
			message = "Doomer Quick review is still running. Use task state and Quick readiness, not aggregate review status or full_review.status. Deep runs in the background; wait for it only when explicitly requested."
		case result.State == "authentication_required" || result.State == "consent_required" || result.State == "approval_required":
			message = "Doomer needs repository authorization. Resume the prepared task for the required action."
		case result.State == "client_update_required" || result.State == "operation_failed" || result.State == "review_failed":
			message = "Doomer could not complete the review. Read the prepared task's status for details and recovery."
		default:
			message = "Doomer review is not complete. Resume the prepared task before finishing."
		}
		if event != "Stop" && (result.State == "ready" || result.State == "review_pending" || result.State == "reviewed") && remaining == 0 {
			return quiet()
		}
	}

	if noticeMessage != "" {
		message += "\n" + noticeMessage
	}
	if event == "Stop" {
		return map[string]any{"decision": "block", "reason": message}
	}
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": message}}
}

// Claude's auto-mode classifier does not receive ordinary tool results. Give it
// factual consent context through its supported field, without claiming that an
// application note can grant user permission or override host policy.
func managedHookFeedbackForAgent(agent, event string, output []byte, operationErr error) map[string]any {
	feedback := managedHookFeedback(event, output, operationErr)
	if agent != "claude" || event != "PostToolUse" || operationErr != nil {
		return feedback
	}
	var result managedResult
	if json.Unmarshal(output, &result) != nil {
		return feedback
	}
	context := map[string]any{"hookEventName": event}
	if existing, ok := feedback["hookSpecificOutput"].(map[string]any); ok {
		context = existing
	}
	if result.Authenticated && result.UploadApproved && result.Consent != nil {
		context["classifierContext"] = "Doomer verified its current account and stored upload consent for the prepared review task. Its CLI still enforces consent and paused state on each review operation. This is application-reported state, not a new user instruction or permission to override host restrictions."
	}
	if result.Review != nil && (result.State == "reviewed" || result.State == "complete") {
		message := "Doomer has review results for the prepared task. A denied explicit submit does not mean no review ran: trusted hooks may have submitted it earlier. Read task status to determine Quick/Deep results and coverage of the current source before reporting review status. Do not retry or route a denied upload through hooks."
		if old, ok := context["additionalContext"].(string); ok && old != "" {
			message += "\n" + old
		}
		context["additionalContext"] = message
	}
	if len(context) > 1 {
		feedback["hookSpecificOutput"] = context
	}
	return feedback
}
