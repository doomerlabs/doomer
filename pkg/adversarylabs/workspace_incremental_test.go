package adversarylabs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestWorkspaceIncrementalSendsOnlyChangedMetadata(t *testing.T) {
	old := sourcesnapshot.Entry{Path: "same.go", Hash: sourcesnapshot.Digest([]byte("same")), Mode: "100644"}
	added := sourcesnapshot.Entry{Path: "new.go", Hash: sourcesnapshot.Digest([]byte("new")), Mode: "100644"}
	removed := sourcesnapshot.Entry{Path: "gone.go", Hash: sourcesnapshot.Digest([]byte("gone")), Mode: "100644"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/repositories" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		var body struct {
			Repository   string `json:"repository"`
			RepositoryID string `json:"repository_id"`
			Base         string `json:"base_snapshot_id"`
			Upserts      []struct {
				Path string `json:"path"`
			} `json:"upserts"`
			Deletes []string `json:"deletes"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Repository != "" || body.RepositoryID != "repo" || body.Base != "base" || len(body.Upserts) != 1 || body.Upserts[0].Path != "new.go" || len(body.Deletes) != 1 || body.Deletes[0] != "gone.go" {
			t.Errorf("not incremental: %+v", body)
		}
		w.Write([]byte(`{"repository_id":"repo","snapshot_id":"next","complete":true}`))
	}))
	defer server.Close()
	c := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
	out, err := c.UploadWorkspaceIncremental(context.Background(), "fixture", "org/repo", []sourcesnapshot.Entry{old, added}, map[string][]byte{old.Hash: []byte("same"), added.Hash: []byte("new")}, &WorkspaceTree{RepositoryID: "repo", SnapshotID: "base", Entries: []sourcesnapshot.Entry{old, removed}}, nil)
	if err != nil || out.SnapshotID != "next" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestWorkspaceIncrementalOnlyFallsBackOnExpiredSnapshot(t *testing.T) {
	for _, status := range []int{http.StatusGone, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					w.WriteHeader(status)
					w.Write([]byte(`{}`))
					return
				}
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				if body["repository"] != "org/repo" {
					t.Error("missing full fallback")
				}
				w.Write([]byte(`{"repository_id":"repo","snapshot_id":"next","complete":true}`))
			}))
			defer server.Close()
			c := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
			_, err := c.UploadWorkspaceIncremental(context.Background(), "fixture", "org/repo", []sourcesnapshot.Entry{}, map[string][]byte{}, &WorkspaceTree{RepositoryID: "repo", SnapshotID: "old"}, nil)
			if status == http.StatusGone {
				if err != nil || calls != 2 {
					t.Fatalf("fallback: calls=%d err=%v", calls, err)
				}
			} else if err == nil || calls != 1 {
				t.Fatalf("retried auth failure: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestWorkspaceBatchesMissingFilesInOnePack(t *testing.T) {
	blobs := map[string][]byte{}
	entries := []sourcesnapshot.Entry{}
	missing := []workspaceMissing{}
	for i := 0; i < 5; i++ {
		data := []byte(fmt.Sprintf("file-%d", i))
		hash := sourcesnapshot.Digest(data)
		blobs[hash] = data
		entries = append(entries, sourcesnapshot.Entry{Path: fmt.Sprintf("%d.go", i), Hash: hash, Mode: "100644"})
		missing = append(missing, workspaceMissing{SHA: hash, Size: len(data), URL: "/api/v1/repositories/blobs/" + hash})
	}
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestPack(t, w, r, func(_ []byte) { uploads++ }) {
			return
		}
		if r.URL.Path == "/api/v1/repositories" {
			json.NewEncoder(w).Encode(map[string]any{"repository_id": "repo", "snapshot_id": "snapshot", "missing": missing, "pack_url": "/api/v1/repositories/packs"})
			return
		}
		w.Write([]byte(`{"repository_id":"repo","snapshot_id":"snapshot","complete":true}`))
	}))
	defer server.Close()
	c := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
	if _, _, err := c.UploadWorkspaceTree(context.Background(), "fixture", "org/repo", entries, blobs); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 {
		t.Fatalf("expected one pack, got %d uploads", uploads)
	}
}

func TestWorkspaceUploadContinuesVerificationWithoutMissingBlobs(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["verification_limit"] != float64(256) {
				t.Error("missing bounded verification request")
			}
		} else if r.URL.Query().Get("verification_limit") != "256" {
			t.Error("finalize lost bounded verification")
		}
		fmt.Fprintf(w, `{"repository_id":"repo","snapshot_id":"snapshot","complete":%t,"checking":%t,"missing":[]}`, requests == 3, requests < 3)
	}))
	defer server.Close()
	client := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
	if _, err := client.UploadWorkspaceIncremental(context.Background(), "token", "org/repo", nil, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if requests != 3 {
		t.Fatalf("expected three verification pages, got %d", requests)
	}
}
