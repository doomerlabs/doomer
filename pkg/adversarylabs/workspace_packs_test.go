package adversarylabs

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func handleTestPack(t *testing.T, w http.ResponseWriter, r *http.Request, accept func([]byte)) bool {
	t.Helper()
	switch r.URL.Path {
	case "/api/v1/repositories/packs":
		var body struct {
			Action string `json:"action"`
			Size   int    `json:"compressed_size"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Action == "create" {
			json.NewEncoder(w).Encode(map[string]any{"id": "test-pack", "upload_url": "http://" + r.Host + "/test-pack", "headers": map[string]string{"content-length": strconv.Itoa(body.Size)}})
		} else {
			w.Write([]byte(`{"complete":true}`))
		}
		return true
	case "/test-pack":
		if r.Method != "PUT" || r.Header.Get("Authorization") != "" {
			t.Error("storage upload method or credentials are wrong")
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if accept != nil {
			accept(data)
		}
		return true
	}
	return false
}
func TestWorkspacePacksRoundTrip(t *testing.T) {
	data := []byte("first source\n")
	hash := sourcesnapshot.Digest(data)
	err := visitWorkspacePacks([]workspaceMissing{{SHA: hash, Size: len(data)}}, map[string][]byte{hash: data}, func(pack workspacePack) error {
		if len(pack.Entries) != 1 || pack.Entries[0].Offset != 0 {
			t.Fatal("bad pack index")
		}
		reader, err := gzip.NewReader(bytes.NewReader(pack.Data))
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		decoded, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(decoded, data) {
			t.Fatal("bad compressed content")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestUnsupportedPackProtocolDoesNotUploadOldBlobs(t *testing.T) {
	data := []byte("source")
	hash := sourcesnapshot.Digest(data)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		json.NewEncoder(w).Encode(map[string]any{"repository_id": "repo", "snapshot_id": "snapshot", "missing": []workspaceMissing{{SHA: hash, Size: len(data), URL: "/api/v1/repositories/blobs/" + hash}}})
	}))
	defer server.Close()
	c := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
	_, err := c.UploadWorkspaceIncremental(context.Background(), "token", "repo", []sourcesnapshot.Entry{{Path: "a", Hash: hash, Mode: "100644"}}, map[string][]byte{hash: data}, nil, nil)
	if err == nil || requests != 1 {
		t.Fatalf("expected protocol error, got %v after %d requests", err, requests)
	}
}
