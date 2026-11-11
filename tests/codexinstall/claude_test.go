package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClaudeTranscriptKeepsOnlyVisibleContent(t *testing.T) {
	raw := `{"type":"system","session_id":"session","message":"CLI status notice"}
{"type":"rate_limit_event","message":"temporary provider status"}
{"type":"user","message":{"content":"a terminal prompt"}}
{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"private reasoning"},{"type":"text","text":"Please approve updates"},{"type":"tool_use","name":"Bash","input":{"command":"doomer prepare"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","content":"{\"state\":\"update_permission_required\"}"}]}}
{"type":"result","session_id":"session","is_error":false}`
	var records []string
	turn, err := readClaudeTurn(strings.NewReader(raw), func(kind, text string) { records = append(records, kind+text) })
	if err != nil || turn.Session != "session" || !turn.UpdateRequested || len(turn.Messages) != 1 {
		t.Fatal(turn, err)
	}
	if strings.Contains(strings.Join(records, "\n"), "private reasoning") {
		t.Fatal(records)
	}
}

func TestClaudePermissionModes(t *testing.T) {
	for _, tc := range []struct {
		config config
		want   string
	}{
		{config{}, "auto"},
		{config{ClaudePermissionMode: "auto"}, "auto"},
		{config{ClaudePermissionMode: "bypassPermissions"}, "bypassPermissions"},
		{config{ClaudePermissionMode: "bypassPermissions", ReviewApprovals: true}, "auto"},
	} {
		if got := claudePermissionMode(tc.config); got != tc.want {
			t.Fatalf("permission mode = %q, want %q", got, tc.want)
		}
	}
}

func TestClaudeDisabledHookReminderWording(t *testing.T) {
	for _, message := range []string{
		"Doomer hooks are disabled. Enable them in /hooks.",
		"The hooks are installed, but your settings currently disable hooks. They are switched off.",
		"Hooks: they're installed but turned off by Claude Code's own settings.",
		"Doomer's hooks are installed, but your Claude settings turn them off.",
		"Doomer hooks:\nDisabled in Claude Code settings. Open /hooks to turn them on.",
		"The hooks are not enabled until you approve.",
		"The hooks are not yet enabled.",
		"The hooks aren't currently enabled.",
		"The hooks aren’t enabled.",
	} {
		if !claudeDisabledHookReminder(message) {
			t.Errorf("missed hook reminder: %s", message)
		}
	}
	for _, message := range []string{
		"Doomer is ready.",
		"Automatic updates are turned off.",
		"The hooks are installed.",
		"Updates are off. Hooks are installed.",
		"Updates are off.\nHooks are installed.",
		"Hooks are enabled.",
		"Auto-updates are not enabled, but hooks are installed.",
		"Hooks are installed, but auto-updates are not enabled.",
	} {
		if claudeDisabledHookReminder(message) {
			t.Errorf("accepted unrelated message: %s", message)
		}
	}
}

func TestClaudeMeterCountsCacheWritesAndPreservesBudget(t *testing.T) {
	m := newMeter(config{Harness: "claude", APIKey: "private-key", Model: "claude-opus-5-5", Budget: .20})
	m.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("x-api-key") != "private-key" {
			t.Fatal("missing authentication")
		}
		if r.URL.Path == "/v1/messages/count_tokens" {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1000}`))}, nil
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["max_tokens"] != float64(4096) || body["stream"] != true {
			t.Fatal(body)
		}
		response := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":200,\"cache_read_input_tokens\":500,\"cache_creation_input_tokens\":300}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":100}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	out := httptest.NewRecorder()
	m.serve(out, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5"}`)))
	if out.Code != 200 || m.usage.Input != 1000 || m.usage.Cached != 500 || m.usage.CacheCreated != 300 || m.usage.Output != 100 || m.usage.Unknown != 0 || math.Abs(m.usage.Reserved-claudeTokenCost(1000, 500, 300, 100)) > 1e-10 {
		t.Fatal(out.Code, m.usage)
	}
	if strings.Contains(out.Body.String(), "private-key") {
		t.Fatal("credential leak")
	}
}

func TestClaudeOpusTokenRates(t *testing.T) {
	// 200 uncached, 500 cached, 300 cache-write, and 100 output tokens.
	if got := claudeTokenCost(1000, 500, 300, 100); math.Abs(got-.0044) > 1e-10 {
		t.Fatalf("Opus token cost = %f, want 0.0044", got)
	}
}

func TestClaudeMeterRefusesOverBudgetAndLongContext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  int64
		budget float64
	}{{"budget", 1000, .0001}, {"context", 98001, .20}} {
		t.Run(tc.name, func(t *testing.T) {
			m := newMeter(config{Harness: "claude", Model: "claude-opus-5-5", Budget: tc.budget})
			m.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/v1/messages/count_tokens" {
					t.Fatal("inference must not start")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"input_tokens":%d}`, tc.input)))}, nil
			})}
			out := httptest.NewRecorder()
			m.serve(out, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5"}`)))
			if out.Code == 200 || m.usage.Requests != 0 {
				t.Fatal(out.Code, m.usage)
			}
			if tc.name == "budget" && m.usage.BudgetRejections != 1 {
				t.Fatal(m.usage)
			}
			if tc.name == "context" && (m.usage.ContextLimitRejections != 1 || m.usage.TokenCountFailures != 0) {
				t.Fatal(m.usage)
			}
		})
	}
}

func TestClaudeMeterRetainsReservationForIncompleteStream(t *testing.T) {
	m := newMeter(config{Harness: "claude", Model: "claude-opus-5-5", Budget: .20})
	m.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"input_tokens":1000}`
		if r.URL.Path == "/v1/messages" {
			body = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1000}}}\n\n"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	out := httptest.NewRecorder()
	m.serve(out, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5"}`)))
	if m.usage.Unknown != 1 || m.usage.Reserved <= 0 || m.usage.Cost != 0 {
		t.Fatal(m.usage)
	}
}

func TestClaudeMeterDoesNotHoldBudgetLockDuringInference(t *testing.T) {
	m := newMeter(config{Harness: "claude", Model: "claude-opus-5-5", Budget: .20})
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := `{"input_tokens":1000}`
		if r.URL.Path == "/v1/messages" {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			body = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1000}}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	go func() {
		defer close(done)
		m.serve(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5"}`)).WithContext(ctx))
	}()
	select {
	case <-started:
	case <-done:
		t.Fatal("request ended before inference")
	case <-ctx.Done():
		t.Fatal("inference did not start within the deadline")
	}
	available := m.mu.TryLock()
	if available {
		m.mu.Unlock()
	}
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("request did not finish within the deadline")
	}
	if !available {
		t.Fatal("inference blocked independent budget accounting")
	}
}

func TestClaudeMeterRetainsReservationOnCancellation(t *testing.T) {
	for _, phase := range []string{"before_headers", "after_message_start"} {
		t.Run(phase, func(t *testing.T) {
			m := newMeter(config{Harness: "claude", Model: "claude-opus-5-5", Budget: .20})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started, done := make(chan struct{}), make(chan struct{})
			m.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/v1/messages/count_tokens" {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"input_tokens":1000}`))}, nil
				}
				if phase == "before_headers" {
					close(started)
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				reader, writer := io.Pipe()
				go func() {
					_, _ = io.WriteString(writer, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1000}}}\n\n")
					close(started)
					<-r.Context().Done()
					_ = writer.CloseWithError(r.Context().Err())
				}()
				return &http.Response{StatusCode: 200, Body: reader}, nil
			})}
			go func() {
				defer close(done)
				m.serve(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"claude-opus-5-5"}`)).WithContext(ctx))
			}()
			select {
			case <-started:
			case <-done:
				t.Fatal("request ended before cancellation point")
			case <-time.After(2 * time.Second):
				t.Fatal("request never reached cancellation point")
			}
			m.mu.Lock()
			reserved := m.usage.Reserved
			m.mu.Unlock()
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled request did not finish")
			}
			if reserved <= 0 || m.usage.Reserved != reserved || m.usage.Unknown != 1 || m.usage.Cost != 0 {
				t.Fatal("cancelled inference changed its conservative charge", reserved, m.usage)
			}
		})
	}
}

func TestHookProposalRecognizesExtractedSetup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		want   bool
	}{
		{"wrapped", `{"hook_setup":{"state":"missing","install_command":["doomer","task","hook-setup","handle","--allow"]}}`, true},
		{"extracted", "{\n  \"state\": \"missing\",\n  \"install_command\": [\n    \"doomer\", \"task\", \"hook-setup\", \"handle\", \"--allow\"\n  ]\n}", true},
		{"other setup", `{"state":"missing","install_command":["doomer","task","policy","handle","--allow"]}`, false},
		{"declined", `{"state":"declined","decline_command":["doomer","task","hook-setup","handle","--decline"]}`, false},
		{"decline echo", `{"state":"declined","install_command":["doomer","task","hook-setup","handle","--decline"]}`, false},
		{"unrelated path", `{"install_command":["/tmp/hook-setup","--allow"]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hookProposal.MatchString(tc.output); got != tc.want {
				t.Fatalf("hook offer = %v, want %v", got, tc.want)
			}
		})
	}
}
