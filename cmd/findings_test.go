package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestFindingsShowUsesSelectedProfileAndPrintsExactMarkdown(t *testing.T) {
	store := isolate(t)
	id := strings.Repeat("a", 64)
	markdown := "# Finding evidence\n\nA suggested fix is one possible approach.\n"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/findings/"+id+"/detail" || r.URL.RawQuery != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer work-token" || r.Header.Get("Accept") != "text/markdown" {
			t.Error("wrong profile credentials or accept header")
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = w.Write([]byte(markdown))
	}))
	defer server.Close()
	for profile, token := range map[string]string{"work": "work-token", "default": "personal-token"} {
		if err := store.SetAuth(adversarylabs.AuthKey(server.URL+"/api", profile), adversarylabs.Auth{Token: token}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := execute(t, "", "profile", "add", "work", "--endpoint", server.URL+"/api"); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "profile", "use", "work"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"findings", "show", id},
		{"--api-url", server.URL + "/api", "--profile", "work", "findings", "show", id},
	} {
		output, err := execute(t, "", args...)
		if err != nil || output != markdown {
			t.Fatalf("finding output: %q, %v", output, err)
		}
	}
	if requests != 2 {
		t.Fatalf("expected one read per invocation; got %d", requests)
	}
}

func TestFindingsShowDoesNotBorrowAnotherProfileOrEndpointLogin(t *testing.T) {
	for _, endpoint := range []string{adversarylabs.DefaultAPIURL, "https://another.example/api"} {
		t.Run(endpoint, func(t *testing.T) {
			store := isolate(t)
			if err := store.SetAuth(adversarylabs.AuthKey(endpoint, "default"), adversarylabs.Auth{Token: "personal-secret"}); err != nil {
				t.Fatal(err)
			}
			output, err := execute(t, "", "--profile", "work", "findings", "show", strings.Repeat("a", 64))
			if err == nil || output != "" || !strings.Contains(err.Error(), "doomer --profile work login") {
				t.Fatalf("missing profile login: %q, %v", output, err)
			}
		})
	}
}

func TestFindingsShowRejectsInvalidIDsAndArgumentCounts(t *testing.T) {
	store := isolate(t)
	if err := store.SetAuth(adversarylabs.AuthKey(adversarylabs.DefaultAPIURL, "default"), adversarylabs.Auth{Token: "token"}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"findings", "show"}, {"findings", "show", "one", "two"},
		{"findings", "show", "../private"}, {"findings", "show", "https://example.test?token=secret"},
	} {
		output, err := execute(t, "", args...)
		if err == nil || output != "" {
			t.Fatalf("accepted invalid arguments: %q, %v", output, err)
		}
	}
}

func TestFindingsShowDoesNotPrintErrorResponseOrCredentials(t *testing.T) {
	for _, status := range []int{401, 403, 404, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			store := isolate(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("private-response credential-secret"))
			}))
			defer server.Close()
			if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: "credential-secret"}); err != nil {
				t.Fatal(err)
			}
			output, err := execute(t, "", "--api-url", server.URL, "findings", "show", strings.Repeat("a", 64))
			if err == nil || output != "" || strings.Contains(err.Error(), "private-response") || strings.Contains(err.Error(), "credential-secret") {
				t.Fatalf("unsafe failure: %q, %v", output, err)
			}
		})
	}
}
