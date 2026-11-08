package adversarylabs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/version"
)

func testNotice() ClientNotice {
	return ClientNotice{ID: "critical-fix", Revision: 1, Component: "cli", Severity: "critical", Message: "Update before submitting new reviews.", RecommendedVersion: "2026.11.9.1", ExpiresAt: time.Now().Add(time.Hour), Blocking: true}
}
func encodeNotice(n ClientNotice) string {
	b, _ := json.Marshal(NoticeEnvelope{Schema: 1, Notices: []ClientNotice{n}})
	return base64.StdEncoding.EncodeToString(b)
}
func TestNoticeTransportMetadataAndError(t *testing.T) {
	n := testNotice()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Doomer-CLI-Version") != version.Version || r.Header.Get("X-Doomer-Notice-Schema") != "1" || r.Header.Get("User-Agent") != "doomer/"+version.Version {
			t.Error("missing runtime metadata")
		}
		w.Header().Set("X-Doomer-Notices", encodeNotice(n))
		w.WriteHeader(409)
		json.NewEncoder(w).Encode(struct {
			NoticeEnvelope
			Error string `json:"error"`
		}{NoticeEnvelope{1, []ClientNotice{n}}, "client_update_required"})
	}))
	defer server.Close()
	root := t.TempDir()
	var output bytes.Buffer
	ctx := WithNoticeCollector(context.Background(), root, server.URL, &output)
	client := NewClientWithBaseURL(ConfigStore{}, server.URL)
	_, err := client.StartWorkspace(ctx, "secret", WorkspaceSubmission{})
	if err == nil || !strings.Contains(err.Error(), "2026.11.9.1") {
		t.Fatalf("missing actionable error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("blocked mutation retried %d times", calls)
	}
	ns := NoticesFromContext(ctx)
	if len(ns) != 1 || !ns[0].Blocking {
		t.Fatal(ns)
	}
	// Background collection persists without claiming the foreground warning was shown.
	var foreground bytes.Buffer
	resumed := WithNoticeCollector(context.Background(), root, server.URL, &foreground)
	if got := NoticesFromContext(resumed); len(got) != 1 || !got[0].Cached {
		t.Fatal(got)
	}
	FlushNotices(resumed)
	if !strings.Contains(foreground.String(), "last known") {
		t.Fatal(foreground.String())
	}
}
func TestNoticeCollectorDedupWithdrawalAndExpiry(t *testing.T) {
	var output bytes.Buffer
	root := t.TempDir()
	ctx := WithNoticeCollector(context.Background(), root, "https://doomer.ai/api", &output)
	c := ctx.Value(noticeContextKey{}).(*NoticeCollector)
	n := testNotice()
	n.Blocking = false
	c.receive(NoticeEnvelope{1, []ClientNotice{n}}, "")
	FlushNotices(ctx)
	FlushNotices(ctx)
	if strings.Count(output.String(), "Doomer critical") != 1 {
		t.Fatal(output.String())
	}
	n.Revision = 2
	c.receive(NoticeEnvelope{1, []ClientNotice{n}}, "")
	FlushNotices(ctx)
	if strings.Count(output.String(), "Doomer critical") != 2 {
		t.Fatal(output.String())
	}
	c.receive(NoticeEnvelope{1, nil}, "")
	if len(NoticesFromContext(ctx)) != 0 {
		t.Fatal("withdrawn notice survived")
	}
	n.ExpiresAt = time.Now().Add(-time.Second)
	c.receive(NoticeEnvelope{1, []ClientNotice{n}}, "")
	if len(NoticesFromContext(ctx)) != 0 {
		t.Fatal("expired notice survived")
	}
}
func TestNoticeValidationAndNoEmptyState(t *testing.T) {
	root := t.TempDir()
	ctx := WithNoticeCollector(context.Background(), root, "https://doomer.ai/api", io.Discard)
	FlushNotices(ctx)
	if _, err := os.Stat(filepath.Join(root, "notices")); !os.IsNotExist(err) {
		t.Fatal("empty local command created cache")
	}
	n := testNotice()
	n.Message = "update\x1b[31m\nsecret"
	c := ctx.Value(noticeContextKey{}).(*NoticeCollector)
	c.receive(NoticeEnvelope{1, []ClientNotice{n}}, "secret")
	got := NoticesFromContext(ctx)[0].Message
	if strings.Contains(got, "\x1b") || strings.Contains(got, "secret") {
		t.Fatal(got)
	}
	c.receive(NoticeEnvelope{99, []ClientNotice{n}}, "")
	if len(NoticesFromContext(ctx)) != 1 {
		t.Fatal("unknown schema cleared valid cache")
	}
	n.RecommendedVersion = "$(touch /tmp/nope)"
	if len(ValidNotices(NoticeEnvelope{1, []ClientNotice{n}}, time.Now())) != 0 {
		t.Fatal("invalid version accepted")
	}
}

type noticeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f noticeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestNoticeMetadataNeverLeaksToExternalUploadURL(t *testing.T) {
	tr := noticeTransport{origin: "https://doomer.ai/api", base: noticeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("X-Doomer-CLI-Version") != "" {
			t.Error("metadata leaked")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	for _, url := range []string{"https://storage.example/upload?signature=secret", "https://doomer.ai/not-api/upload"} {
		req, _ := http.NewRequest("PUT", url, nil)
		if _, err := tr.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBlockedPackInitiationDoesNotTransferBytes(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/repositories/packs" || r.Header.Get("X-Doomer-CLI-Version") != version.Version {
			t.Error("unexpected request", r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"client_update_required"}`)
	}))
	defer server.Close()
	client := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
	_, err := client.uploadWorkspacePack(context.Background(), "token", workspacePack{Data: []byte("source bytes")}, nil)
	failure, ok := err.(*ResponseError)
	if !ok || failure.Code != "client_update_required" || calls != 1 {
		t.Fatalf("expected initiation rejection only: calls=%d error=%v", calls, err)
	}
}
