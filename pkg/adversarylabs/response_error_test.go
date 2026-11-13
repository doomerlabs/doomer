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
	if !strings.HasPrefix(err.Error(), "missing_review_scope: request failed: 400") || !errors.Is(err, ErrMissingReviewScope) || strings.Contains(err.Error(), "PRIVATE_SOURCE") {
		t.Fatalf("scope error: %v", err)
	}
}

func TestResponseErrorGivesRecoveryWithoutTrustingServerInstructions(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{401, "doomer login --device"},
		{403, "account has access"},
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
