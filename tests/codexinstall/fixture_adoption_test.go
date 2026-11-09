package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestFixtureDistinguishesAdoptionMetadataFromRepositoryUploads(t *testing.T) {
	f := &fixture{Approved: true}
	event := adversarylabs.ActivationEvent{Stage: "approved", InstallationID: "install", AttemptID: "attempt", Repository: "local/opaque", Agent: "codex"}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	send := func(path string, body []byte) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer fixture-account-token")
		out := httptest.NewRecorder()
		f.serve(out, req)
		return out.Code
	}
	if status := send("/v2/installations/activate", raw); status != http.StatusOK || f.Uploads != 0 || len(f.Adoptions) != 1 {
		t.Fatalf("metadata counted as source upload: status=%d uploads=%d events=%d", status, f.Uploads, len(f.Adoptions))
	}
	f.Approved = false
	if status := send("/v2/installations/activate", raw); status != http.StatusForbidden || len(f.Adoptions) != 1 {
		t.Fatal("unapproved adoption accepted", status)
	}
	f.Approved = true
	if status := send("/v2/installations/activate", []byte(`{"stage":"approved","source":"private code"}`)); status != http.StatusBadRequest {
		t.Fatal("unexpected payload accepted", status)
	}
	if status := send("/v1/repositories", []byte(`{}`)); status != http.StatusOK || f.Uploads != 1 {
		t.Fatal("repository upload no longer counted", status, f.Uploads)
	}
	if status := send("/unexpected-upload", []byte(`{}`)); status != http.StatusNotFound || f.Uploads != 2 {
		t.Fatal("unexpected upload no longer counted", status, f.Uploads)
	}
}
