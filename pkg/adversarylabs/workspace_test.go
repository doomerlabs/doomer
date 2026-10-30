package adversarylabs

import (
	"context"
	"encoding/json"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceUploadUsesOnlyValidatedBlobRoutes(t *testing.T) {
	data := []byte("source")
	sha := sourcesnapshot.Digest(data)
	puts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing auth")
		}
		switch r.URL.Path {
		case "/api/v1/repositories":
			json.NewEncoder(w).Encode(map[string]any{"repository_id": "repo", "snapshot_id": "snapshot", "missing": []map[string]any{{"sha256": sha, "size": len(data), "upload_url": "/api/v1/repositories/blobs/" + sha}}, "finalize_url": "/api/v1/repositories/repo/snapshots/snapshot/finalize"})
		case "/api/v1/repositories/blobs/" + sha:
			puts++
			w.Write([]byte(`{}`))
		case "/api/v1/repositories/repo/snapshots/snapshot/finalize":
			w.Write([]byte(`{"repository_id":"repo","snapshot_id":"snapshot","complete":true,"missing":[]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
	repo, id, err := client.UploadWorkspaceTree(context.Background(), "test-token", "org/repo", []sourcesnapshot.Entry{{Path: "source.go", Hash: sha, Mode: "100644"}}, map[string][]byte{sha: data})
	if err != nil || repo != "repo" || id != "snapshot" || puts != 1 {
		t.Fatalf("upload failed: %s %s %d %v", repo, id, puts, err)
	}
}
func TestWorkspaceRejectsForeignUploadURL(t *testing.T) {
	data := []byte("source")
	sha := sourcesnapshot.Digest(data)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"repository_id": "repo", "snapshot_id": "snapshot", "missing": []map[string]any{{"sha256": sha, "size": len(data), "upload_url": "https://example.com/steal"}}})
	}))
	defer server.Close()
	client := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
	_, _, err := client.UploadWorkspaceTree(context.Background(), "private", "org/repo", []sourcesnapshot.Entry{{Path: "file", Hash: sha, Mode: "100644"}}, map[string][]byte{sha: data})
	if err == nil || !strings.Contains(err.Error(), "unexpected upload") {
		t.Fatal(err)
	}
}
