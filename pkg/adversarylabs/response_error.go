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

var requestIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var validationFieldPattern = regexp.MustCompile(`^(body|repository|repository_id|pull_request|review_node_id|head_sha|comments(\[[0-9]{1,2}\](\.(adversary|package_name|package_version|finding_id|rule_id|path|body))?)?)$`)

// responseError accepts only bounded, known diagnostics. An upstream proxy or
// server can echo credentials and request bodies even in JSON error messages,
// so arbitrary messages, headers, and raw response bodies must never be logged.
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
		case "missing_review_scope":
			safeCode = body.Error
			message += ": " + body.Error
			cause = ErrMissingReviewScope
		case "Content does not match SHA-256":
			message += ": uploaded file checksum mismatch (the server may have received a truncated body)"
		case "invalid_request", "invalid_user_request", "invalid_json", "unauthorized", "insufficient_scope", "repository_mismatch",
			"not_found", "submission_limit", "idempotency_conflict", "payload_too_large", "invalid_snapshot",
			"digest_mismatch", "source_head_mismatch", "submission_expired_or_finalized", "not_ready", "review_identity_conflict", "review_cancelled":
			safeCode = body.Error
			message += ": " + body.Error
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
	if cause != nil {
		message = cause.Error() + ": " + message
	}
	if safeCode == "client_update_required" {
		for _, n := range ValidNotices(body.NoticeEnvelope, time.Now()) {
			if n.Blocking {
				message += " (recommended version " + n.RecommendedVersion + ")"
				break
			}
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
