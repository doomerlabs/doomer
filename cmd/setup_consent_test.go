package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSetupDesktopModes(t *testing.T) {
	for _, tc := range []struct {
		os   string
		env  map[string]string
		want bool
	}{
		{"darwin", nil, true}, {"darwin", map[string]string{"SSH_CONNECTION": "remote"}, false},
		{"linux", nil, false}, {"linux", map[string]string{"DISPLAY": ":0"}, true},
		{"linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true},
		{"linux", map[string]string{"DISPLAY": ":0", "SSH_TTY": "/dev/pts/0"}, false},
	} {
		if got := setupDesktop(tc.os, func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
}

func TestSetupPrivacyURLFollowsDestination(t *testing.T) {
	for _, api := range []string{"http://localhost:3000/api", "https://doomer.ai/api"} {
		s := setupConsent{API: api}
		if got := s.PrivacyURL(); got != strings.TrimSuffix(api, "/api")+"/privacy" {
			t.Fatal(got)
		}
	}
}

func TestBrowserSetupRejectsInvalidDestination(t *testing.T) {
	for _, api := range []string{"", "file:///tmp/service", "https://user:password@example.test/api"} {
		_, err := browserSetupConsent(context.Background(), setupConsent{API: api}, io.Discard, func(context.Context, string) error { t.Fatal("opened browser for invalid destination"); return nil })
		if err == nil {
			t.Fatalf("accepted %q", api)
		}
	}
}

func TestSetupApprovalRequiresNonceAndIsSingleUse(t *testing.T) {
	for _, decision := range []string{"allow", "cancel"} {
		result := make(chan bool, 1)
		s := setupConsent{Nonce: strings.Repeat("a", 64)}
		handler := setupApprovalHandler(s, "127.0.0.1:1234", result)
		for _, path := range []string{"/complete?decision=" + decision, "/complete?decision=" + decision + "&nonce=wrong", "/other?decision=" + decision + "&nonce=" + s.Nonce} {
			req := httptest.NewRequest("GET", "http://127.0.0.1:1234"+path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != http.StatusForbidden || len(result) != 0 {
				t.Fatal(response.Code)
			}
		}
		path := "http://127.0.0.1:1234/complete?decision=" + decision + "&nonce=" + s.Nonce
		wrongHost := httptest.NewRequest("GET", path, nil)
		wrongHost.Host = "evil.test"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, wrongHost)
		if response.Code != http.StatusForbidden {
			t.Fatal(response.Code)
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 || <-result != (decision == "allow") {
			t.Fatal(response.Code)
		}
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != http.StatusGone || len(result) != 0 {
			t.Fatal(response.Code)
		}
	}
}

func TestBrowserSetupRoundTrip(t *testing.T) {
	var out bytes.Buffer
	s := setupConsent{API: "http://localhost:3000/api", Profile: "local", AllRepositories: true}
	approved, err := browserSetupConsent(context.Background(), s, &out, func(ctx context.Context, link string) error {
		u, _ := url.Parse(link)
		if u.Hostname() != "127.0.0.1" || u.Path != "/consent" || u.Query().Get("nonce") == "" {
			return fmt.Errorf("unexpected consent URL: %s", link)
		}
		page, err := http.Get(link)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(page.Body)
		page.Body.Close()
		if err != nil || !strings.Contains(string(body), "http://localhost:3000/api") || !strings.Contains(string(body), "task prompt") {
			return fmt.Errorf("missing disclosure: %s", body)
		}
		callback := *u
		callback.Path = "/complete"
		q := callback.Query()
		q.Set("decision", "allow")
		callback.RawQuery = q.Encode()
		response, err := http.Get(callback.String())
		if err != nil {
			return err
		}
		defer response.Body.Close()
		_, err = io.Copy(io.Discard, response.Body)
		return err
	})
	if err != nil || !approved {
		t.Fatalf("%v %v", approved, err)
	}
}

func TestTerminalSetupConsentCancelsBlockedRead(t *testing.T) {
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- terminalSetupConsent(ctx, input) }()
	cancel()
	select {
	case approved := <-done:
		if approved {
			t.Fatal("cancellation must not approve uploads")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not unblock setup")
	}
}

func TestTerminalSetupConsentRequiresYes(t *testing.T) {
	for _, text := range []string{"no\n", "\n", "", "yes please\n"} {
		if terminalSetupConsent(context.Background(), strings.NewReader(text)) {
			t.Fatalf("accepted %q", text)
		}
	}
	if !terminalSetupConsent(context.Background(), strings.NewReader("yes\n")) {
		t.Fatal("rejected yes")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if terminalSetupConsent(ctx, strings.NewReader("yes\n")) {
		t.Fatal("accepted after cancellation")
	}
}
