package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorBrowserForwardsSessionCode(t *testing.T) {
	const code = "example.session-code"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Code string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code != code {
			t.Errorf("received code %q, error %v", body.Code, err)
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	err := sendDoctorBrowserResult(context.Background(), server.URL, code, []string{})
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("expected server rejection, got %v", err)
	}
}

func TestDoctorBrowserEndpoint(t *testing.T) {
	t.Setenv("DOOMER_HOME", t.TempDir())
	t.Setenv("DOOMER_API_URL", "http://[::1]:3040/api")
	repo := t.TempDir()
	for _, origin := range []string{"https://doomer.ai", "http://[::1]:3040"} {
		endpoint, err := doctorBrowserEndpoint(origin, repo, false)
		if err != nil || endpoint != origin+"/api/docs/doctor-session" {
			t.Fatalf("valid origin %q returned %q, %v", origin, endpoint, err)
		}
	}
	for _, origin := range []string{"https://unconfigured.example", "https://doomer.ai:8080", "http://[::1]:3041"} {
		if _, err := doctorBrowserEndpoint(origin, repo, false); err == nil || !strings.Contains(err.Error(), "--allow-unconfigured-browser-origin") {
			t.Fatalf("accepted unconfigured origin %q", origin)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".doomer.env"), []byte("DOOMER_API_URL=https://configured.example/api\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := doctorBrowserEndpoint("https://configured.example", repo, false); err != nil {
		t.Fatal(err)
	}
	if endpoint, err := doctorBrowserEndpoint("https://dev.example", repo, true); err != nil || endpoint != "https://dev.example/api/docs/doctor-session" {
		t.Fatalf("override returned %q, %v", endpoint, err)
	}
	for _, origin := range []string{"http://dev.example", "https://dev.example/api", "https://user@dev.example", "https://dev.example?name=check"} {
		if _, err := doctorBrowserEndpoint(origin, repo, true); err == nil {
			t.Fatalf("override accepted invalid origin %q", origin)
		}
	}
}
