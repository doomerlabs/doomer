package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMeterCountsUsageAndLimitsOutput(t *testing.T) {
	m := newMeter(config{APIKey: "private-test-key", Model: "gpt-6.1-terra", Budget: .2})
	if m.client.Timeout != 0 {
		t.Fatal("streaming request has a whole-response timeout")
	}
	m.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/responses/input_tokens" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1000}`))}, nil
		}
		if r.URL.String() != "https://api.openai.com/v1/responses" || r.Header.Get("Authorization") != "Bearer private-test-key" {
			t.Fatal("incorrect upstream")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["max_output_tokens"] != float64(4096) || body["service_tier"] != "default" || body["stream"] != true {
			t.Fatal(body)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1000,\"output_tokens\":100,\"input_tokens_details\":{\"cached_tokens\":500}}}}\n\n"))}, nil
	})}
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-6.1-terra","input":"hello"}`))
	out := httptest.NewRecorder()
	m.serve(out, req)
	if m.usage.Requests != 1 || m.usage.Unknown != 0 || m.usage.Input != 1000 || m.usage.Cost != tokenCost(1000, 500, 100) || math.Abs(m.usage.Cost-m.usage.Reserved) > 1e-9 {
		t.Fatal(m.usage)
	}
	if strings.Contains(out.Body.String(), "private-test-key") {
		t.Fatal("key exposed")
	}
}

func TestMeterRejectsBeforeSpending(t *testing.T) {
	m := newMeter(config{Model: "gpt-6.1-terra", Budget: .001})
	m.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/responses/input_tokens" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1000}`))}, nil
		}
		t.Fatal("unexpected paid request")
		return nil, nil
	})}
	out := httptest.NewRecorder()
	m.serve(out, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-6.1-terra"}`)))
	if out.Code != 429 || m.usage.Requests != 0 {
		t.Fatal(out.Code, m.usage)
	}
}

func TestFixtureRequiresApprovalAndValidVerifier(t *testing.T) {
	verifier := "test-verifier"
	sum := sha256.Sum256([]byte(verifier))
	f := &fixture{}
	request := `{"request":"test-request","resumable":true,"codeChallenge":"` + base64.RawURLEncoding.EncodeToString(sum[:]) + `"}`
	f.serve(httptest.NewRecorder(), httptest.NewRequest("POST", "http://localhost/v1/setup/grants", strings.NewReader(request)))
	poll := func(verifier string) *httptest.ResponseRecorder {
		out := httptest.NewRecorder()
		f.serve(out, httptest.NewRequest("POST", "http://localhost/v1/setup/grants/poll", strings.NewReader(`{"request":"test-request","codeVerifier":"`+verifier+`"}`)))
		return out
	}
	if out := poll(verifier); !strings.Contains(out.Body.String(), "pending") {
		t.Fatal(out.Body.String())
	}
	f.Approved = true
	if out := poll("incorrect"); out.Code != 403 {
		t.Fatal(out.Code)
	}
	if out := poll(verifier); !strings.Contains(out.Body.String(), "fixture-account-token") {
		t.Fatal(out.Body.String())
	}
}

func TestUnknownUsageKeepsReservation(t *testing.T) {
	m := newMeter(config{Model: "gpt-6.1-terra", Budget: .2})
	m.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"input_tokens":1000}`
		if r.URL.Path == "/v1/responses" {
			body = "data: {\"type\":\"error\"}\n\n"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	m.serve(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-6.1-terra"}`)))
	if m.usage.Unknown != 1 || m.usage.Reserved <= 0 || m.usage.Cost != 0 {
		t.Fatal(m.usage)
	}
}

func TestReportRedactsCredentials(t *testing.T) {
	got := redact("key=private-secret token=fixture-account-token", "private-secret")
	if strings.Contains(got, "private-secret") || strings.Contains(got, "fixture-account-token") {
		t.Fatal(got)
	}
}

func TestReplayEscapesRecordedContent(t *testing.T) {
	path := t.TempDir() + "/replay.html"
	r := report{Events: []recordedEvent{{Kind: "Codex", Text: "</script><script>alert('unsafe')</script>"}}}
	if err := writeReplay(path, r); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "</script><script>alert") || !strings.Contains(string(data), `\u003c/script\u003e`) {
		t.Fatal("recorded content was not safely encoded")
	}
}

func TestCodexFailureCannotBecomeSuccessfulTurn(t *testing.T) {
	stream := "{\"type\":\"turn.failed\"}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"Installed successfully\"}}\n"
	turn, err := readCodexTurn(strings.NewReader(stream), func(string, string) {})
	if err != nil || !turn.Failed {
		t.Fatal(turn, err)
	}
}

func TestCodexUpdateStateIgnoresJSONWhitespace(t *testing.T) {
	for _, space := range []string{"", " ", "  ", "\n\t"} {
		raw, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"type": "command_execution", "aggregated_output": "{\"state\":" + space + "\"update_permission_required\"}"}})
		turn, err := readCodexTurn(bytes.NewReader(raw), func(string, string) {})
		if err != nil || !turn.UpdateRequested {
			t.Fatal(turn, err)
		}
	}
}

func TestScenarioNames(t *testing.T) {
	for _, name := range scenarioNames {
		if !validScenario(name) {
			t.Fatal(name)
		}
	}
	if validScenario("anything") {
		t.Fatal("unknown scenario accepted")
	}
}

func TestReviewFixtureRequiresAccountAndRecordsFeedback(t *testing.T) {
	f := &fixture{Approved: true, Findings: true}
	call := func(path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		f.serve(out, req)
		return out
	}
	if out := call("/v1/reviews/workspace", `{"review_id":"review-test"}`, "wrong"); out.Code != 403 {
		t.Fatal(out.Code)
	}
	if out := call("/v1/reviews/workspace", `{"review_id":"review-test"}`, "fixture-account-token"); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	body := `{"lead_id":"` + strings.Repeat("a", 64) + `","status":"dismissed","reason":"verified fixture greeting","agrees":false}`
	if out := call("/v1/reviews/workspace/review-test/feedback", body, "fixture-account-token"); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	if len(f.Reviews) != 1 || len(f.Feedback) != 1 {
		t.Fatal(f.Reviews, f.Feedback)
	}
	f.FailSubmit = true
	if out := call("/v1/reviews/workspace", `{"review_id":"failed-review"}`, "fixture-account-token"); out.Code != 503 {
		t.Fatal(out.Code)
	}
	if len(f.Reviews) != 1 {
		t.Fatal("failed submission created a review")
	}
}

func TestLocalProcessCleanupIsScopedToExecutablePath(t *testing.T) {
	root := filepath.Join(os.TempDir(), "test run")
	output := "100 " + filepath.Join(root, "home", "bin", "codex") + " app-server\n" +
		"101 " + filepath.Join(root+"-other", "bin", "codex") + "\n" +
		"102 /bin/sh -c " + filepath.Join(root, "home", "bin", "codex") + "\n" +
		"103 /Users/person/.codex/bin/codex app-server\n"
	pids := localProcessIDs(output, root)
	if len(pids) != 1 || pids[0] != 100 {
		t.Fatal(pids)
	}
}

func TestRelocateHomeEnvironmentPreservesSiblingPaths(t *testing.T) {
	old := filepath.Join(os.TempDir(), "old-home")
	fresh := filepath.Join(os.TempDir(), "new-home")
	sep := string(os.PathListSeparator)
	bin := filepath.Join(old, "bin")
	for _, entry := range []struct{ input, want string }{
		{"HOME=" + old, "HOME=" + fresh},
		{"CODEX_HOME=" + filepath.Join(old, ".codex"), "CODEX_HOME=" + filepath.Join(fresh, ".codex")},
		{"TMPDIR=" + old + "-scratch", "TMPDIR=" + old + "-scratch"},
		{"CDPATH=" + old + "other", "CDPATH=" + old + "other"},
		{"PATH=" + bin + sep + old + "-other", "PATH=" + filepath.Join(fresh, "bin") + sep + old + "-other"},
	} {
		if got := relocateHomeEnvironment(entry.input, old, fresh); got != entry.want {
			t.Fatal(entry.input, got, entry.want)
		}
	}
}

func TestMeterCancellationReleasesAccountingLock(t *testing.T) {
	m := newMeter(config{Model: "gpt-6.1-terra", Budget: .2})
	m.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/responses/input_tokens" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1}`))}, nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-6.1-terra"}`)).WithContext(ctx)
	m.serve(httptest.NewRecorder(), r)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.usage.Unknown != 1 || m.usage.Reserved == 0 {
		t.Fatal("cancelled request lost its conservative budget reservation", m.usage)
	}
}

func TestInstallRequiresConfiguredCodexPolicy(t *testing.T) {
	for _, policyState := range []string{"missing", "configured"} {
		t.Run(policyState, func(t *testing.T) {
			root := t.TempDir()
			home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
			files := map[string]string{
				filepath.Join(repo, ".agents/skills/doomer-review/SKILL.md"):   "doomer-protocol",
				filepath.Join(repo, ".claude/skills/doomer-review/SKILL.md"):   "doomer-protocol",
				agentHooksPath(config{Harness: "codex"}, home):                 "--managed-task-adapter",
				filepath.Join(home, ".config/doomer/managed/tasks/setup.json"): `{"handle":"setup","agent":"codex"}`,
				filepath.Join(home, ".local/bin/doomer"):                       "#!/bin/sh\nprintf '%s\\n' '{\"state\":\"ready\",\"software_ready\":true,\"authenticated\":true,\"upload_approved\":true,\"codex_policy_status\":{\"state\":\"" + policyState + "\"}}'\n",
			}
			for path, content := range files {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ready, checks := inspectInstall(context.Background(), repo, home, nil, false, "codex")
			want := policyState == "configured"
			if ready != want || checks["codex_policy_configured"] != want {
				t.Fatalf("ready=%v checks=%v, expected configured=%v", ready, checks, want)
			}
		})
	}
}
