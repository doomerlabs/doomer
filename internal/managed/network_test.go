package managed

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestNetworkErrorPreservesSafeServiceDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_user_request","message":"PRIVATE_SOURCE signed-url?token=secret"}`))
	}))
	defer server.Close()
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, server.URL)
	_, err := client.StartWorkspace(context.Background(), "secret", adversarylabs.WorkspaceSubmission{})
	err = NetworkError("review_submission", err)
	var response *adversarylabs.ResponseError
	if !errors.As(err, &response) || response.StatusCode != 400 || response.Code != "invalid_user_request" {
		t.Fatal(err)
	}
	if !strings.Contains(err.Error(), "invalid_user_request") || strings.Contains(err.Error(), "PRIVATE_SOURCE") || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}
