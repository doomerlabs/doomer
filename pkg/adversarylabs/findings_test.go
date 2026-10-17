package adversarylabs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type findingTransport func(*http.Request) (*http.Response, error)

func (f findingTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type brokenFindingReader struct{}

func (brokenFindingReader) Read([]byte) (int, error) { return 0, errors.New("private read failure") }
func (brokenFindingReader) Close() error             { return nil }

func TestFindingDetailValidatesResponseAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
		broken, success         bool
	}{
		{name: "markdown", status: 200, contentType: "text/markdown; charset=utf-8", body: "# Evidence\n", success: true},
		{name: "exact limit", status: 200, contentType: "text/markdown", body: strings.Repeat("x", maxFindingDetailBytes), success: true},
		{name: "oversized", status: 200, contentType: "text/markdown", body: strings.Repeat("x", maxFindingDetailBytes+1)},
		{name: "html login", status: 200, contentType: "text/html", body: "private login page"},
		{name: "missing content type", status: 200},
		{name: "invalid content type", status: 200, contentType: "invalid;="},
		{name: "read error", status: 200, contentType: "text/markdown", broken: true},
		{name: "expired login", status: 401, body: "private error"},
		{name: "unauthorized project", status: 403, body: "private error"},
		{name: "flag off or missing finding", status: 404, body: "private error"},
		{name: "server failure", status: 503, body: "private error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := Client{BaseURL: "https://example.test/api", HTTP: &http.Client{Transport: findingTransport(func(request *http.Request) (*http.Response, error) {
				if request.Header.Get("Authorization") != "Bearer scoped-secret" || request.Header.Get("Accept") != "text/markdown" || request.URL.RawQuery != "" {
					t.Error("incorrect authentication or content negotiation")
				}
				var body io.ReadCloser = io.NopCloser(strings.NewReader(tc.body))
				if tc.broken {
					body = brokenFindingReader{}
				}
				return &http.Response{StatusCode: tc.status, Body: body, Header: http.Header{"Content-Type": {tc.contentType}}}, nil
			})}}
			body, err := client.FindingDetail(context.Background(), "scoped-secret", strings.Repeat("a", 64))
			if tc.success {
				if err != nil || body != tc.body {
					t.Fatalf("markdown changed: length=%d, %v", len(body), err)
				}
			} else if err == nil || body != "" || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "scoped-secret") {
				t.Fatalf("unsafe error response: length=%d, %v", len(body), err)
			}
		})
	}
}

func TestFindingDetailRejectsBadInputsBeforeHTTP(t *testing.T) {
	client := Client{BaseURL: "https://example.test/api", HTTP: &http.Client{Transport: findingTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid input reached transport")
		return nil, nil
	})}}
	for _, id := range []string{"", "../private", strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 65)} {
		if _, err := client.FindingDetail(context.Background(), "secret", id); err == nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
	for _, endpoint := range []string{"http://remote.example/api", "https://example.test?token=secret"} {
		client.BaseURL = endpoint
		if _, err := client.FindingDetail(context.Background(), "secret", strings.Repeat("a", 64)); err == nil {
			t.Fatal("accepted unsafe API URL")
		}
	}
}

func TestFindingDetailNetworkErrorsAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if canceled {
			cancel()
		}
		client := Client{BaseURL: "https://example.test/api", HTTP: &http.Client{Transport: findingTransport(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network error containing private credentials")
		})}}
		body, err := client.FindingDetail(ctx, "secret", strings.Repeat("a", 64))
		cancel()
		if err == nil || body != "" || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe transport error: %q, %v", body, err)
		}
		if canceled && !errors.Is(err, context.Canceled) {
			t.Fatalf("lost cancellation: %v", err)
		}
	}
}

func TestFindingDetailDoesNotFollowCrossOriginRedirects(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) { targetCalls++ }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, target.URL, http.StatusFound)
	}))
	defer source.Close()
	client := NewClientWithBaseURL(ConfigStore{}, source.URL)
	if _, err := client.FindingDetail(context.Background(), "secret", strings.Repeat("a", 64)); err == nil || targetCalls != 0 {
		t.Fatalf("redirect followed: requests=%d, error=%v", targetCalls, err)
	}
}
