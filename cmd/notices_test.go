package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNoticesPreservesBodyWithoutCollectorOrHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(adversarylabs.NoticeEnvelope{Schema: 1, Notices: []adversarylabs.ClientNotice{{ID: "body-notice", Revision: 1, Component: "cli", Severity: "recommended", Message: "Install the recommended release.", RecommendedVersion: "2026.11.9.1", ExpiresAt: time.Now().Add(time.Hour)}}})
	}))
	defer server.Close()
	api := server.URL
	cmd := newNoticesCommand(&api)
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "body-notice") {
		t.Fatal(output.String())
	}
}

func TestNoticeAcknowledgementCommandIsOfflineAndRevisionScoped(t *testing.T) {
	isolate(t)
	n := adversarylabs.ClientNotice{ID: "update", Revision: 2, Component: "cli", Severity: "recommended", Message: "Display this update", RecommendedVersion: "2026.11.9.1", ExpiresAt: time.Now().Add(time.Hour)}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(adversarylabs.NoticeEnvelope{Schema: 1, Notices: []adversarylabs.ClientNotice{n}})
	}))
	defer server.Close()
	fetch := func() adversarylabs.ClientNotice {
		t.Helper()
		out, err := execute(t, "", "--api-url", server.URL, "notices", "--json")
		if err != nil {
			t.Fatal(err)
		}
		// The first line is JSON; the pending notice can also be printed to stderr.
		var result outputEnvelope[adversarylabs.NoticeEnvelope]
		if err := json.NewDecoder(strings.NewReader(out)).Decode(&result); err != nil {
			t.Fatal(err, out)
		}
		if len(result.Data.Notices) != 1 {
			t.Fatal(out)
		}
		return result.Data.Notices[0]
	}
	if got := fetch(); !got.NotifyUser || len(got.AcknowledgeCommand) == 0 {
		t.Fatal(got)
	}
	if got := fetch(); !got.NotifyUser {
		t.Fatal("tool output marked notice shown")
	}
	for _, args := range [][]string{
		{"notices", "--ack", n.ID},
		{"notices", "--revision", "2"},
		{"notices", "--ack", "", "--revision", "2"},
		{"notices", "--ack", n.ID, "--revision", "1"},
		{"notices", "--ack", n.ID, "--revision", "2", "--defer", n.ID},
	} {
		if _, err := execute(t, "", append([]string{"--api-url", server.URL}, args...)...); err == nil {
			t.Fatal("invalid acknowledgement accepted", args)
		}
	}
	server.Close() // Acknowledgement must succeed even when the server is unavailable.
	out, err := execute(t, "", "--api-url", server.URL, "notices", "--ack", n.ID, "--revision", "2")
	if err != nil || !strings.Contains(out, `"displayed":true`) {
		t.Fatal(err, out)
	}
	if calls != 2 {
		t.Fatal("acknowledgement or validation contacted server", calls)
	}
	// Reopen a collector to verify the durable receipt rather than just command output.
	ctx := adversarylabs.WithNoticeCollector(context.Background(), os.Getenv("DOOMER_CONFIG_DIR"), server.URL, nil)
	if got := adversarylabs.NoticesFromContext(ctx); len(got) != 1 || got[0].NotifyUser {
		t.Fatal("receipt not persisted", got)
	}
}
