package cmd

import (
	"bytes"
	"encoding/json"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"net/http"
	"net/http/httptest"
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
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "body-notice") {
		t.Fatal(output.String())
	}
}
