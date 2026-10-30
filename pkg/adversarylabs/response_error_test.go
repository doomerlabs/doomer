package adversarylabs

import (
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
