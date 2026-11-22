package doctor

import (
	"cmp"
	"strings"
	"time"
	"unicode"

	"github.com/doomerlabs/doomer/internal/watch"
)

func RedactDescription(description string) string {
	return (collector{}).clean(description, 0).(string)
}

// Checks summarize evidence without attributing another session's review to this chat.
func (c collector) checks(report object, checkoutFound bool, session string) []any {
	items := []any{}
	add := func(code, message, next string) {
		item := object{"code": code, "message": message}
		if next != "" {
			item["next"] = next
		}
		items = append(items, item)
	}
	if !checkoutFound {
		add("checkout_unavailable", "Could not identify a Git checkout.", "Run Doctor from the affected checkout to inspect its review activity.")
		return items
	}
	var event, review, previousSuccess object
	activation := "pending"
	detail := "No current Codex hook receipt was verified for this session."
	for _, value := range report["watch_roots"].([]any) {
		root := value.(map[string]any)
		if session != "" && activation != "ready" {
			status, message := watch.CodexActivationStatus(text(root, "path"), c.repo, session)
			if status == "ready" || status == "error" {
				activation, detail = status, message
			}
		}
		for _, entry := range root["recent_hook_events"].([]any) {
			hook := entry.(map[string]any)
			repository, _ := hook["Repository"].(string)
			if repository != "" && when(hook, "At").After(when(event, "At")) {
				event = hook
			}
		}
		success := nested(root, "last_success")
		if when(success, "completed_at").After(when(previousSuccess, "completed_at")) {
			previousSuccess = success
		}
		candidate := nested(root, "review")
		if when(candidate, "Updated").After(when(review, "Updated")) {
			review = candidate
		}
	}
	if session != "" {
		add("codex_session_"+activation, display(detail), "")
	}
	if event == nil {
		add("hook_evidence_missing", "No recent hook activity was recorded for this checkout.", "Check the Doomer hook and its trust in the affected agent profile, then run a read-only tool in that session and check again.")
	} else {
		add("hook_activity_recorded", "Most recent recorded hook activity: "+when(event, "At").Format(time.RFC3339Nano)+" "+display(text(event, "Event")+" "+text(event, "Status")), "")
	}
	switch status := text(review, "AssessmentStatus"); {
	case status == "ready":
		ref := cmp.Or(display(text(review, "ReviewID")), "ID unavailable")
		delivery := ""
		if review["handoff_recorded"] == true {
			delivery = " Doomer recorded a handoff to the agent."
		}
		add("review_completed", "Review completed, chat delivery unverified ("+ref+")."+delivery, "If the result did not appear, look up that review ID.")
	case text(review, "FailureCode") == "agent_response_timeout" || text(review, "FailureCode") == "timeout":
		add("assessment_timed_out", "Assessment timed out", "Open the review ID from the local report and check its last confirmed step.")
	case strings.HasPrefix(status, "fail") || text(review, "FailureCode") != "":
		add("review_failed", "Most recent recorded Quick review failed ("+display(cmp.Or(text(review, "FailureCode"), status))+").", "Use the review ID from the local report when troubleshooting.")
	case status != "":
		add("review_status_recorded", "Most recent recorded review status: "+display(status)+".", "")
	default:
		add("review_evidence_missing", "No Quick review status was recorded for this checkout.", "Verify the hook and watcher in the affected session before trying a small coding change.")
	}
	if version := text(previousSuccess, "cli_version"); version != "" && version != text(report, "cli_version") {
		add("cli_changed_since_success", "Last recorded successful Quick review used Doomer "+display(version)+". This Doctor is running "+display(text(report, "cli_version"))+".", "Check which executable the affected agent profile launches.")
	}
	return items
}

func text(value object, key string) string {
	s, _ := value[key].(string)
	return s
}

func when(value object, key string) time.Time {
	timestamp, _ := time.Parse(time.RFC3339Nano, text(value, key))
	return timestamp
}

func display(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}
