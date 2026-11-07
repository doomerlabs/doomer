package managed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestNetworkErrorRetainsHostWithoutURLSecrets(t *testing.T) {
	original := &url.Error{Op: "PUT", URL: "https://user:password@storage.example:443/private?token=secret#fragment", Err: errors.New("proxy echoed secret")}
	err := NetworkError("upload", original)
	var connection *ConnectionError
	if !errors.As(err, &connection) || connection.Host != "storage.example" {
		t.Fatal(err)
	}
	for _, secret := range []string{"user", "password", "private", "token", "secret", "fragment", "443"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("leaked %q in %v", secret, err)
		}
	}
	var unsafe *url.Error
	if errors.As(err, &unsafe) {
		t.Fatal("retained unsafe URL error")
	}
}

func TestNetworkErrorOnlySuggestsPolicyForUnknownForbiddenResponse(t *testing.T) {
	for _, code := range []string{"", "insufficient_scope"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprintf(w, `{"error":%q,"message":"private secret"}`, code)
			}))
			defer server.Close()
			client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, server.URL)
			_, err := client.StartWorkspace(context.Background(), "secret", adversarylabs.WorkspaceSubmission{})
			err = NetworkError("review_submission", err)
			var connection *ConnectionError
			if errors.As(err, &connection) != (code == "") {
				t.Fatal(err)
			}
			var response *adversarylabs.ResponseError
			if !errors.As(err, &response) || response.StatusCode != http.StatusForbidden || response.Host != "127.0.0.1" {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
		})
	}
}
