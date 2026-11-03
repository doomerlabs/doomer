package watch

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (r Runner) markRetrying(path string) {
	var feed Feed
	if Read(FeedPath(r.Root, path), &feed) == nil && feed.AssessmentStatus == "unavailable" {
		feed.AssessmentStatus = "retrying"
		_ = Write(FeedPath(r.Root, path), feed)
	}
}

func reviewProgressMessage(feed Feed) string {
	switch feed.AssessmentStatus {
	case "uploading":
		return "Doomer is still uploading the repository; no assessment is available yet."
	case "preparing":
		return "Doomer is preparing repository context on the server; no assessment is available yet."
	case "analyzing":
		return "Doomer is still preparing or analyzing this review; no assessment is available yet."
	case "retrying":
		return "Doomer could not complete the review and is retrying."
	case "unavailable":
		reason := map[string]string{"upload_timeout": "the repository upload timed out", "upload_failed": "the repository upload failed", "analysis_timeout": "analysis timed out", "repository_context": "the server could not prepare repository context", "repository_context_timeout": "repository context preparation timed out", "missing_review_scope": "the server could not identify the edited code to review", "timeout": "analysis timed out", "model_request_failed": "the analysis service failed", "model_configuration": "the analysis service is not configured"}[feed.FailureCode]
		if reason == "" {
			reason = "the review service was unavailable"
		}
		return fmt.Sprintf("Doomer review failed: %s. It is not a completed or pending assessment.", reason)
	}
	return ""
}

// Report persistent local capture failures through the completing tool hook:
// there may be no review state or Stop event for this checkout yet.
func captureFailureNotice(root, turnFile string, owner Owner, err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "worktree changed while capturing") {
		return ""
	}
	code, reason := "capture_failed", "local snapshot capture failed"
	if strings.Contains(err.Error(), "symlinks") || strings.Contains(err.Error(), "symlink") {
		code, reason = "capture_symlink", "the checkout contains an unsupported source symlink"
	}
	message := ""
	_ = Locked(root, owner.Session, func() error {
		var turn Turn
		if Read(turnFile, &turn) != nil || turn.Owner != owner {
			return nil
		}
		key := "capture-error:" + code
		for _, delivered := range turn.Delivered {
			if delivered == key {
				return nil
			}
		}
		turn.Delivered = append(turn.Delivered, key)
		if err := Write(turnFile, turn); err != nil {
			return err
		}
		message = "Doomer could not start a review because " + reason + ". If no later completed review arrives, briefly report this concrete failure in your final answer instead of calling the review pending or unconfirmed. Continue the coding task; do not repeat setup or these instructions."
		return nil
	})
	return message
}
