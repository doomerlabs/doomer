package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestReviewGetResults(t *testing.T) {
	store := isolate(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/reviews/submissions/review-123" || r.Header.Get("Authorization") != "Bearer work-token" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"id":"review-123","status":"completed","digest":"abc","result":{"summary":"Review summary","conclusion":"failure","findings":[{"path":"main.go","line":3,"body":"Fix this","extra":{"preserved":true}}],"codeContexts":[{"path":"main.go","lines":[1,2]}]},"error":"partial failure"}`)
	}))
	defer api.Close()
	if err := store.SetAuth(adversarylabs.AuthKey(api.URL, "work"), adversarylabs.Auth{Token: "work-token"}); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"get", "status"} {
		out, err := execute(t, "", "--api-url", api.URL, "--profile", "work", "review", alias, "review-123", "--format", "json")
		if err != nil {
			t.Fatal(err)
		}
		var result outputEnvelope[adversarylabs.ReviewStatus]
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		if result.Command != "review get" || result.Data.Status != "completed" || result.Data.Result.Summary != "Review summary" || result.Data.Result.Conclusion != "failure" || result.Data.Error != "partial failure" || !strings.Contains(string(result.Data.Result.Findings[0]), `"preserved":true`) || len(result.Data.Result.CodeContexts) == 0 {
			t.Fatalf("lost result data: %s", out)
		}
	}
	out, err := execute(t, "", "--api-url", api.URL, "--profile", "work", "review", "get", "review-123")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Review review-123: completed", "Conclusion: failure", "Summary: Review summary", "Findings: 1", "Fix this", "Error: partial failure"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
}

func TestReviewGetValidationAndUnavailableResults(t *testing.T) {
	store := isolate(t)
	calls := 0
	status := 200
	payload := `{"id":"job","status":"queued"}`
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(status); fmt.Fprint(w, payload) }))
	defer api.Close()
	args := []string{"--api-url", api.URL, "review", "get", "job"}
	if out, err := execute(t, "", args...); err == nil || !strings.Contains(err.Error(), "not logged in") || strings.Contains(out, "Usage:") {
		t.Fatalf("auth: %v", err)
	}
	if err := store.SetAuth(adversarylabs.AuthKey(api.URL, "default"), adversarylabs.Auth{Token: "token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", append(args, "--format", "xml")...); err == nil {
		t.Fatal("accepted invalid format")
	}
	if calls != 0 {
		t.Fatal("validation contacted server")
	}
	out, err := execute(t, "", args...)
	if err != nil || !strings.Contains(out, "queued") {
		t.Fatalf("queued: %s %v", out, err)
	}
	payload = `{"id":"job","status":"failed","error":"worker unavailable"}`
	out, err = execute(t, "", args...)
	if err != nil || !strings.Contains(out, "worker unavailable") {
		t.Fatalf("failed lookup: %s %v", out, err)
	}
	for _, code := range []int{401, 403, 404, 500} {
		status = code
		if out, err := execute(t, "", args...); err == nil {
			t.Fatalf("accepted HTTP %d", code)
		} else if strings.Contains(out, "Usage:") {
			t.Fatalf("runtime error printed usage: %s", out)
		}
	}
	status = 200
	for _, body := range []string{`not json`, `{"id":"other","status":"queued"}`, `{"id":"job"}`, strings.Repeat("x", (1<<20)+1)} {
		payload = body
		if _, err := execute(t, "", args...); err == nil {
			t.Fatal("accepted invalid response")
		}
	}
}
