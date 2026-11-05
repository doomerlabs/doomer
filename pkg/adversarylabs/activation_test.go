package adversarylabs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type reportingScope struct{ enabled bool }

func (s *reportingScope) SetActivationReporting(v bool) { s.enabled = v }
func TestActivationCapabilityRequiresAuthenticatedServerAcknowledgement(t *testing.T) {
	for _, response := range []string{`{"url":"https://example.test/cli/setup"}`, `{"url":"https://example.test/cli/setup","activationReporting":true}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/setup/grants" || r.Header.Get("Authorization") != "Bearer token" {
				t.Errorf("unexpected setup request: %s %s", r.Method, r.URL.Path)
				http.Error(w, "invalid request", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(response))
		}))
		scope := &reportingScope{}
		_, err := (Client{BaseURL: server.URL}).CreateSetupGrant(context.Background(), "token", scope)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if scope.enabled != strings.Contains(response, `"activationReporting":true`) {
			t.Fatalf("unexpected reporting acknowledgement: %s", response)
		}
	}
}
func TestActivationUsesAuthenticatedDedicatedEndpoint(t *testing.T) {
	event := ActivationEvent{Stage: "activated", InstallationID: "random", Repository: "github.com/org/repo", Agent: "codex", Client: "codex-desktop", OS: "darwin", Architecture: "arm64", CLIVersion: "2026.11.5.2", OccurredAt: "2026-11-05T12:00:00Z"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/installations/activate" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		var got ActivationEvent
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got != event {
			t.Errorf("event mismatch %v", err)
		}
		w.Write([]byte(`{"accepted":true}`))
	}))
	defer server.Close()
	if err := (Client{BaseURL: server.URL}).ReportActivation(context.Background(), "token", event); err != nil {
		t.Fatal(err)
	}
}
