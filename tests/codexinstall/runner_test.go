package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMeterCountsUsageAndLimitsOutput(t *testing.T) {
	m := newMeter(config{APIKey: "private-test-key", Model: "gpt-6.1-terra", Budget: .2})
	m.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/responses/input_tokens" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1000}`))}, nil
		}
		if r.URL.String() != "https://api.openai.com/v1/responses" || r.Header.Get("Authorization") != "Bearer private-test-key" {
			t.Fatal("incorrect upstream")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["max_output_tokens"] != float64(4096) || body["service_tier"] != "default" {
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
