package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestProjectList(t *testing.T) {
	for _, tc := range []struct {
		name, response, command, want string
	}{
		{"account projects", `{"team":{"slug":"zebra","name":"Zebra"},"teams":[{"slug":"zebra","name":"Zebra"},{"slug":"alpha","name":"Alpha Project"}]}`, "ls", "SLUG NAME alpha Alpha Project zebra Zebra"},
		{"scoped token", `{"team":{"slug":"alpha","name":"Alpha Project"}}`, "list", "SLUG NAME * alpha Alpha Project"},
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

func TestProjectUseAndScope(t *testing.T) {
	store := isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"teams":[{"id":"alpha-id","slug":"alpha","name":"Alpha"},{"id":"beta-id","slug":"beta","name":"Beta"}]}`))
	}))
	defer server.Close()
	key := adversarylabs.AuthKey(server.URL, "work")
	if err := store.SetAuth(key, adversarylabs.Auth{Token: "token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "--api-url", server.URL, "--profile", "work", "project", "use", "beta"); err != nil {
		t.Fatal(err)
	}
	selected, err := store.DefaultProject(key)
	if err != nil || selected != "beta-id" {
		t.Fatalf("selection: %q, %v", selected, err)
	}
	out, err := execute(t, "", "--api-url", server.URL, "--profile", "work", "project", "ls")
	if err != nil || !strings.Contains(strings.Join(strings.Fields(out), " "), "* beta Beta") {
		t.Fatalf("listing: %q, %v", out, err)
	}
	for _, other := range []string{adversarylabs.AuthKey(server.URL, "default"), adversarylabs.AuthKey("https://other.example/api", "work")} {
		if value, err := store.DefaultProject(other); err != nil || value != "" {
			t.Fatalf("selection leaked: %q, %v", value, err)
		}
	}
	if _, err := execute(t, "", "--api-url", server.URL, "--profile", "work", "project", "use", "unknown"); err == nil {
		t.Fatal("accepted unavailable project")
	}
	if value, _ := store.DefaultProject(key); value != "beta-id" {
		t.Fatal("invalid selection replaced default")
	}
}

func TestResolveReviewProject(t *testing.T) {
	for _, tc := range []struct{ name, teams, selected, explicit, want, failure string }{
		{name: "only project", teams: `[{"slug":"alpha"}]`, want: "alpha"},
		{name: "saved project ID survives rename", teams: `[{"id":"one","slug":"renamed"},{"id":"two","slug":"beta"}]`, selected: "one", want: "renamed"},
		{name: "explicit override", teams: `[]`, selected: "old", explicit: "override", want: "override"},
		{name: "multiple require selection", teams: `[{"slug":"alpha"},{"slug":"beta"}]`, failure: "no default project"},
		{name: "no projects", teams: `[]`, failure: "no default project"},
		{name: "stale selection", teams: `[{"slug":"alpha"}]`, selected: "removed", failure: "no longer available"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := isolate(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.explicit != "" {
					t.Error("explicit override should not fetch projects")
				}
				if r.Header.Get("Authorization") != "Bearer token" {
					t.Error("wrong credentials")
				}
				_, _ = w.Write([]byte(`{"teams":` + tc.teams + `}`))
			}))
			defer server.Close()
			if tc.selected != "" {
				if err := store.SetDefaultProject(adversarylabs.AuthKey(server.URL, "work"), tc.selected); err != nil {
					t.Fatal(err)
				}
			}
			got, err := resolveReviewProject(context.Background(), application.Dependencies{Auth: store}, server.URL, "work", tc.explicit, "token")
			if tc.failure != "" {
				if err == nil || !strings.Contains(err.Error(), tc.failure) {
					t.Fatalf("got %q, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
