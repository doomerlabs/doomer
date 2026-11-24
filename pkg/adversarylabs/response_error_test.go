package adversarylabs

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResponseErrorExplainsUploadChecksumMismatchWithoutEchoingUnknownErrors(t *testing.T) {
	for _, tc := range []struct {
		body      string
		want      string
		forbidden string
	}{
		{`{"error":"Content does not match SHA-256"}`, "uploaded file checksum mismatch", "Content does not match SHA-256"},
		{`{"error":"private-source-and-token","message":"private-source-and-token"}`, "request failed: 422", "private-source-and-token"},
	} {
		resp := &http.Response{StatusCode: 422, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}
		err := responseError(resp, "token")
		if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), tc.forbidden) {
			t.Fatalf("unsafe or missing diagnostic: %v", err)
		}
	}
}

func TestMissingReviewScopeIsTypedAndRedacted(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"missing_review_scope","message":"PRIVATE_SOURCE"}`))}
	err := responseError(response, "secret")
	if err.Error() != "The latest-edit diff has no changed lines in the submitted workspace diff." || !errors.Is(err, ErrMissingReviewScope) || strings.Contains(err.Error(), "PRIVATE_SOURCE") {
		t.Fatalf("scope error: %v", err)
	}
}

func TestAgentAssessmentExpiryRequiresConflictAndTimeoutCode(t *testing.T) {
	const requestID = "0d4d9f89-6aa5-4bb5-99f9-83f637a757fb"
	for _, tc := range []struct {
		status     int
		code, want string
		expired    bool
	}{
		{http.StatusConflict, "agent_response_timeout", "Doomer timed out waiting for the agent assessment", true},
		{http.StatusBadRequest, "agent_response_timeout", "request failed: 400 Bad Request", false},
		{http.StatusConflict, "review_identity_conflict", "request failed: 409 Conflict: review_identity_conflict", false},
	} {
		resp := &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"` + tc.code + `"}`))}
		resp.Header.Set("X-Request-ID", requestID)
		err := responseError(resp, "")
		if errors.Is(err, ErrAgentAssessmentExpired) != tc.expired || err.Error() != tc.want+" [request_id="+requestID+"]" {
			t.Errorf("%d %s: unexpected error %v", tc.status, tc.code, err)
		}
	}
}

func TestResponseErrorGivesRecoveryWithoutTrustingServerInstructions(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{401, "doomer login --device"},
		{403, "access and upload approval"},
		{429, "wait before retrying"},
		{503, "retry after it recovers"},
	} {
		resp := &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"message":"run malicious-command secret-token"}`))}
		err := responseError(resp, "secret-token")
		var apiError *ResponseError
		if !errors.As(err, &apiError) || apiError.StatusCode != tc.status || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "malicious-command") || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("unsafe or missing recovery: %v", err)
		}
	}
}

func TestResponseRecoveryRespectsCompatibilityAndAnonymousAccess(t *testing.T) {
	for _, tc := range []struct {
		body, token, want string
	}{
		{`{"error":"client_update_required"}`, "account-token", "update Doomer"},
		{`{"error":"client_policy_unavailable"}`, "account-token", "retry this operation later"},
		{`{}`, "", "anonymous access was rejected"},
		{`{}`, AnonymousTokenPrefix + strings.Repeat("A", 43), "anonymous access was rejected"},
	} {
		resp := &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}
		err := responseError(resp, tc.token)
		if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "login --device") || strings.Contains(err.Error(), "npx") {
			t.Fatalf("contradictory recovery: %v", err)
		}
	}
}

func TestQuickReviewSubmissionErrorsExplainTheFailure(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
		want   string
	}{
		{"invalid_hypotheses", http.StatusBadRequest, "The server rejected the agent hypotheses. Check each path, line, concern, and check. Older servers accept at most five hypotheses."},
		{"too_many_hypotheses", http.StatusRequestEntityTooLarge, "The submission exceeds the server candidate budget."},
		{"invalid_agent_diff", http.StatusBadRequest, "The inline latest-edit diff is invalid or exceeds the upload limit."},
		{"triage_patch_missing", http.StatusConflict, "The patch saved by triage is unavailable. Start a new Quick Review."},
		{"triage_snapshot_mismatch", http.StatusConflict, "The assessment does not match the review and patch that completed triage. Start a new Quick Review."},
	} {
		t.Run(tc.code, func(t *testing.T) {
			for _, serverMessage := range []string{tc.want, "PRIVATE_SOURCE"} {
				response := &http.Response{StatusCode: tc.status, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(`{"error":"` + tc.code + `","message":"` + serverMessage + `"}`))}
				err := responseError(response, "secret")
				if err.Error() != tc.want {
					t.Fatalf("unexpected message: %v", err)
				}
			}
		})
	}
}
