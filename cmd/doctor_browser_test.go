package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorBrowserEndpoint(t *testing.T) {
	t.Setenv("DOOMER_HOME", t.TempDir())
	t.Setenv("DOOMER_API_URL", "http://[::1]:3040/api")
	code := strings.Repeat("x", 43)
	repo := t.TempDir()
	for _, origin := range []string{"https://doomer.ai", "http://[::1]:3040"} {
		endpoint, err := doctorBrowserEndpoint(origin, code, repo)
		if err != nil || endpoint != origin+"/api/docs/doctor-session" {
			t.Fatalf("valid origin %q returned %q, %v", origin, endpoint, err)
		}
	}
	for _, origin := range []string{"https://unconfigured.example", "https://doomer.ai:8080", "http://[::1]:3041"} {
		if _, err := doctorBrowserEndpoint(origin, code, repo); err == nil {
			t.Fatalf("accepted unconfigured origin %q", origin)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, ".doomer.env"), []byte("DOOMER_API_URL=https://configured.example/api\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := doctorBrowserEndpoint("https://configured.example", code, repo); err != nil {
		t.Fatal(err)
	}
}
