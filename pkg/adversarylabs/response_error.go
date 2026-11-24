package adversarylabs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ResponseError contains only allowlisted server diagnostics, never a raw body.
type ResponseError struct {
	Notices    []ClientNotice
	StatusCode int
	Host       string
	Code       string
	message    string
	cause      error
}

func (e *ResponseError) Error() string { return e.message }
func (e *ResponseError) Unwrap() error { return e.cause }

// ErrMissingReviewScope identifies a rejected submission with no usable edit scope.
var ErrMissingReviewScope = errors.New("missing_review_scope")

// ErrAgentAssessmentExpired identifies an attempt the service has closed.
var ErrAgentAssessmentExpired = errors.New("Doomer timed out waiting for the agent assessment")

var requestIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var validationFieldPattern = regexp.MustCompile(`^(body|repository|repository_id|pull_request|review_node_id|head_sha|comments(\[[0-9]{1,2}\](\.(adversary|package_name|package_version|finding_id|rule_id|path|body))?)?)$`)

// These are server-authored validation messages that are safe to relay. An
// unexpected message can contain private request content, so use local wording
// for known failures when talking to a server that omits or changes the message.
const legacyInvalidHypothesesMessage = "The agent hypotheses are invalid. Include up to five hypotheses with a changed path, line, concern, and check."

var workspaceValidationMessages = map[string]string{
	"missing_review_scope":     "The latest-edit diff has no changed lines in the submitted workspace diff.",
	"invalid_hypotheses":       "The agent hypotheses are invalid. Each hypothesis needs a changed path, line, concern, and check.",
	"too_many_hypotheses":      "The submission exceeds the server candidate budget.",
	"invalid_triage_utf8":      "The uploaded triage diff is not valid UTF-8.",
	"invalid_workspace_review": "The workspace review request is missing required fields or contains invalid JSON.",
	"invalid_review_scope":     "The workspace review scope is invalid.",
	"invalid_agent_diff":       "The inline latest-edit diff is invalid or exceeds the upload limit.",
	"invalid_agent_assessment": "A complete agent assessment is required to submit a triaged review.",
	"missing_agent_hypotheses": "The agent assessment must include a list of hypotheses, even if it is empty.",
	"hypothesis_outside_diff":  "An agent hypothesis points outside the latest-edit diff.",
	"invalid_agent_handoff":    "The agent assessment has invalid review details or a mismatched hypothesis count.",
	"area_assessment_required": "The agent must submit one assessment for each area selected during triage.",
	"invalid_area_assessment":  "An area assessment is missing evidence, has an invalid status, or names an unexpected or duplicate area.",
	"area_hypothesis_required": "The assessment identifies a concern but includes no hypothesis to score.",
	"invalid_triage_request":   "The triage request is missing required fields or contains invalid JSON.",
	"invalid_triage_part":      "A part of the triage diff is invalid.",
	"invalid_triage_manifest":  "The list of uploaded triage parts is invalid.",
	"triage_checksum_mismatch": "A triage upload checksum did not match. Upload the patch again.",
	"triage_too_many_hunks":    "The latest-edit diff exceeds the 256-hunk Quick Review limit. Split the change into smaller reviews.",
	"triage_patch_missing":     "The patch saved by triage is unavailable. Start a new Quick Review.",
	"triage_snapshot_mismatch": "The assessment does not match the review and patch that completed triage. Start a new Quick Review.",
}

// Limit diagnostics to known errors because upstream responses can echo credentials and source.
func responseError(resp *http.Response, token string) error {
	var cause error
	safeCode := ""
	message := fmt.Sprintf("request failed: %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	var body struct {
		Error     string `json:"error"`
		Code      string `json:"code"`
		Field     string `json:"field"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
		NoticeEnvelope
	}
	const maxErrorBytes = 8 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes+1))
	if err == nil && len(data) <= maxErrorBytes && json.Unmarshal(data, &body) == nil {
		switch body.Error {
		case "client_update_required":
			safeCode = body.Error
			message += ": this CLI version is no longer supported for this operation; update Doomer using your existing installation method (https://doomer.ai/updates)"
		case "client_policy_unavailable":
			safeCode = body.Error
			message += ": update policy temporarily unavailable; retry this operation later"
		case "triage_diff_too_large", "triage_budget_exceeded", "triage_line_too_large", "triage_binary_unassessed", "triage_parts_missing", "triage_checksum_mismatch", "invalid_triage_utf8", "invalid_triage_manifest", "invalid_triage_part", "invalid_triage_request", "triage_timeout", "triage_unavailable", "invalid_triage_scores":
			safeCode = body.Error
			message += ": " + body.Error + "; Quick review is incomplete"
		case "missing_review_scope":
			safeCode = body.Error
			message += ": " + body.Error
			cause = ErrMissingReviewScope
		case "agent_response_timeout":
			if resp.StatusCode == http.StatusConflict {
				safeCode = body.Error
				message = ErrAgentAssessmentExpired.Error()
				cause = ErrAgentAssessmentExpired
			}
		case "Content does not match SHA-256":
			message += ": uploaded file checksum mismatch (the server may have received a truncated body)"
		case "invalid_request", "invalid_user_request", "invalid_json", "unauthorized", "insufficient_scope", "repository_mismatch", "access_denied",
			"invalid_evidence_context", "not_found", "submission_limit", "idempotency_conflict", "payload_too_large", "invalid_snapshot",
			"digest_mismatch", "source_head_mismatch", "submission_expired_or_finalized", "not_ready", "review_identity_conflict", "review_cancelled", "review_superseded", "triage_snapshot_mismatch":
			safeCode = body.Error
			message += ": " + body.Error
		}
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusRequestEntityTooLarge {
			if wording, ok := workspaceValidationMessages[body.Error]; ok {
				safeCode = body.Error
				message = wording
			}
			if body.Error == "invalid_hypotheses" && body.Message == legacyInvalidHypothesesMessage {
				message = legacyInvalidHypothesesMessage
			}
			if body.Error == "triage_diff_too_large" && body.Message == "The latest-edit diff exceeds the 8 MiB Quick Review limit. Split the change into smaller reviews." {
				message = body.Message
			}
		}
		if body.Error == "reviews_disabled" {
			message += ": hosted reviews are not enabled for this project"
		}
		if body.Error == "invalid_request" {
			reason := ""
			switch body.Code {
			case "required":
				reason = "required field is missing or empty"
			case "invalid_type":
				reason = "field has an invalid type"
			case "invalid_format":
				reason = "field has an invalid format"
			case "out_of_range":
				reason = "field is outside the allowed range"
			case "too_long":
				reason = "field exceeds the length limit"
			case "too_many_items":
				reason = "array exceeds the item limit"
			case "duplicate_finding":
				reason = "finding_id must be unique per adversary and package in a review"
			}
			// Older servers return only a message for the original collision.
			if reason == "" && body.Message == "finding_id must be unique per review" {
				reason = body.Message
			}
			if reason != "" {
				message += ": " + reason
				if validationFieldPattern.MatchString(body.Field) {
					message += " (" + body.Field + ")"
				}
			}
		}
	}
	requestID := resp.Header.Get("X-Request-ID")
	if !requestIDPattern.MatchString(requestID) {
		requestID = body.RequestID
	}
	if requestIDPattern.MatchString(requestID) {
		message += " [request_id=" + requestID + "]"
	}
	if safeCode == "client_update_required" {
		for _, n := range ValidNotices(body.NoticeEnvelope, time.Now()) {
			if n.Blocking && n.RecommendedVersion != "" {
				message += " (recommended version " + n.RecommendedVersion + ")"
				break
			}
		}
	}
	// An explicit compatibility failure already supplies its own recovery.
	// Otherwise derive advice from the status and credential type, never prose.
	if safeCode != "client_update_required" && safeCode != "client_policy_unavailable" {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			if token == "" || IsAnonymousToken(token) {
				message += "; anonymous access was rejected; check the configured service and upload approval (https://doomer.ai/docs/troubleshooting)"
			} else {
				message += "; sign in again with doomer login --device using the same service and profile"
			}
		case http.StatusForbidden:
			message += "; check your access and upload approval for this operation before retrying"
		case http.StatusTooManyRequests:
			message += "; wait before retrying; a rate-limited request is not a completed review"
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, http.StatusInternalServerError:
			message += "; the service could not complete this request; retry after it recovers and do not report the review as complete"
		}
	}
	if token != "" {
		message = strings.ReplaceAll(message, token, "[redacted]")
	}
	host := ""
	if resp.Request != nil && resp.Request.URL != nil {
		host = resp.Request.URL.Hostname()
	}
	notices := ValidNotices(body.NoticeEnvelope, time.Now())
	if token != "" {
		for i := range notices {
			notices[i].Message = strings.ReplaceAll(notices[i].Message, token, "[redacted]")
		}
	}
	return &ResponseError{Notices: notices, StatusCode: resp.StatusCode, Host: host, Code: safeCode, message: message, cause: cause}
}
