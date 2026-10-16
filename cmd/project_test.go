package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestProjectList(t *testing.T) {
	for _, tc := range []struct {
		name, response, command, want string
	}{
		{"account projects", `{"team":{"slug":"zebra","name":"Zebra"},"teams":[{"slug":"zebra","name":"Zebra"},{"slug":"alpha","name":"Alpha Project"}]}`, "ls", "SLUG NAME alpha Alpha Project zebra Zebra"},
		{"scoped token", `{"team":{"slug":"alpha","name":"Alpha Project"}}`, "list", "SLUG NAME alpha Alpha Project"},
		{"empty", `{"teams":[]}`, "ls", "No projects found."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := isolate(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/auth/whoami" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer work-token" {
					t.Error("wrong account credentials")
				}
				_, _ = w.Write([]byte(tc.response))
			}))
			defer server.Close()
			for profile, token := range map[string]string{"work": "work-token", "default": "personal-token"} {
				if err := store.SetAuth(adversarylabs.AuthKey(server.URL, profile), adversarylabs.Auth{Token: token}); err != nil {
					t.Fatal(err)
				}
			}
			out, err := execute(t, "", "--api-url", server.URL, "--profile", "work", "project", tc.command)
			if err != nil || strings.Join(strings.Fields(out), " ") != tc.want {
				t.Fatalf("project list: %q, %v", out, err)
			}
		})
	}
}

func TestProjectListRequiresSelectedProfileLogin(t *testing.T) {
	store := isolate(t)
	if err := store.SetAuth(adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "default"), adversarylabs.Auth{Token: "personal-token"}); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "", "--profile", "work", "project", "ls")
	if err == nil || !strings.Contains(err.Error(), "doomer --profile work login") || out != "" {
		t.Fatalf("missing credentials: %q, %v", out, err)
	}
}

func TestProjectListAPIError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			store := isolate(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":"secret-token"}`))
			}))
			defer server.Close()
			if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: "secret-token"}); err != nil {
				t.Fatal(err)
			}
			out, err := execute(t, "", "--api-url", server.URL, "project", "ls")
			if err == nil || out != "" || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("API error: %q, %v", out, err)
			}
		})
	}
}
