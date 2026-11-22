package cmd

import (
	"strings"
	"testing"
)

func TestDoctorBrowserEndpoint(t *testing.T) {
	t.Setenv("DOOMER_API_URL", "http://[::1]:3040/api")
	code := strings.Repeat("x", 43)
	for _, origin := range []string{"https://doomer.ai", "http://[::1]:3040"} {
		endpoint, err := doctorBrowserEndpoint(origin, code)
		if err != nil || endpoint != origin+"/api/docs/doctor-session" {
			t.Fatalf("valid origin %q returned %q, %v", origin, endpoint, err)
		}
	}
	for _, origin := range []string{"https://unconfigured.example", "https://doomer.ai:8080", "http://[::1]:3041"} {
		if _, err := doctorBrowserEndpoint(origin, code); err == nil {
			t.Fatalf("accepted unconfigured origin %q", origin)
		}
	}
}
