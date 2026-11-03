package watch

import "fmt"

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
