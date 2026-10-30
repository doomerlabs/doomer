package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestWhoamiVerifiesSelectedProfile(t *testing.T) {
	store := isolate(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/v1/auth/whoami" || r.Header.Get("Authorization") != "Bearer chosen-token" {
			t.Errorf("wrong request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"name":"Marc","email":"marc@example.test","team":{"name":"Example"},"subscription":{"plan":"Pro","status":"active"}}`)
	}))
	defer server.Close()
	if _, err := execute(t, "", "profile", "add", "work", "--endpoint", server.URL); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "work"), adversarylabs.Auth{Token: "chosen-token"}); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		out, err := execute(t, "", "whoami", "--profile", "work", "--format", format)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "chosen-token") {
			t.Fatal("credentials leaked")
		}
		if format == "json" {
			var response outputEnvelope[identityInfo]
			if err = json.Unmarshal([]byte(out), &response); err != nil {
				t.Fatal(err)
			}
			if response.Data.Profile != "work" || response.Data.Service != server.URL || response.Data.Status != "authenticated" || response.Data.Account.Email != "marc@example.test" {
				t.Fatal(out)
			}
		} else {
			for _, want := range []string{"Profile: work", server.URL, "Marc <marc@example.test>", "Team: Example", "Plan: Pro (active)"} {
				if !strings.Contains(out, want) {
					t.Fatal(out)
				}
			}
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestWhoamiAnonymousDoesNotContactServiceOrCreateCredentials(t *testing.T) {
	store := isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("anonymous identity contacted service") }))
	defer server.Close()
	if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "work"), adversarylabs.Auth{Token: "another-profile"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "", "--api-url", server.URL, "--profile", "anonymous-local", "whoami")
	if err != nil || !strings.Contains(out, "Profile: anonymous-local") || !strings.Contains(out, "anonymously") {
		t.Fatalf("%s %v", out, err)
	}
	after, err := os.ReadFile(store.Path)
	if err != nil || string(after) != string(before) {
		t.Fatal("identity query changed credentials")
	}
}

func TestWhoamiDoesNotDowngradeRejectedOrExpiredLogin(t *testing.T) {
	store := isolate(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	key := adversarylabs.AuthKey(server.URL, "work")
	if err := store.SetAuth(key, adversarylabs.Auth{Token: "revoked"}); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "", "--api-url", server.URL, "--profile", "work", "whoami")
	if err == nil || strings.Contains(out, "anonymously") || !strings.Contains(err.Error(), "work") {
		t.Fatalf("%s %v", out, err)
	}
	if err = store.SetAuth(key, adversarylabs.Auth{Token: "expired", ExpiresAt: time.Now().Add(-time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	_, err = execute(t, "", "--api-url", server.URL, "--profile", "work", "whoami")
	if err == nil || !strings.Contains(err.Error(), "expired") || calls != 1 {
		t.Fatalf("%v, calls=%d", err, calls)
	}
}
