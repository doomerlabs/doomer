package adversarylabs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestWorkspaceUploadReportsBytesBeforeServerAcknowledgement(t *testing.T) {
	content := []byte(strings.Repeat("x", 512*1024))
	hash := sourcesnapshot.Digest(content)
	progressObserved := make(chan struct{})
	var once sync.Once
	var partial atomic.Bool
	var total atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/repositories/packs" {
			handleTestPack(t, w, r, nil)
			return
		}
		switch r.URL.Path {
		case "/api/v1/repositories":
			json.NewEncoder(w).Encode(map[string]any{"repository_id": "repo", "snapshot_id": "snapshot", "pack_url": "/api/v1/repositories/packs", "missing": []map[string]any{{"sha256": hash, "size": len(content), "upload_url": "/api/v1/repositories/blobs/" + hash}}})
		case "/test-pack":
			if r.ContentLength <= 0 || r.ContentLength >= int64(len(content)) || r.Header.Get("Authorization") != "" {
				t.Error("progress tracking changed content length")
			}
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Error(err)
			}
			select {
			case <-progressObserved:
			case <-time.After(2 * time.Second):
				t.Error("no progress before acknowledgement")
			}
			w.Write([]byte(`{}`))
		case "/api/v1/repositories/repo/snapshots/snapshot/finalize":
			w.Write([]byte(`{"repository_id":"repo","snapshot_id":"snapshot","complete":true}`))
		default:
			t.Error("unexpected request")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	client := NewClientWithBaseURL(ConfigStore{}, server.URL+"/api")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := client.UploadWorkspaceIncremental(ctx, "token", "repo", []sourcesnapshot.Entry{{Path: "file.txt", Hash: hash, Mode: "100644"}}, map[string][]byte{hash: content}, nil, func(stage string, files, bytes int, _ time.Duration) {
		if stage == "transfer_total" {
			total.Store(int64(bytes))
		}
		if stage == "transfer" && bytes > 0 {
			partial.Store(true)
			once.Do(func() { close(progressObserved) })
		}
	})
	if err != nil || !partial.Load() || total.Load() <= 0 || total.Load() >= int64(len(content)) {
		t.Fatalf("progress missing: %v", err)
	}
}

func TestWorkspaceProgressReaderCountsBytesWithoutChangingBody(t *testing.T) {
	count := 0
	ctx := context.WithValue(context.Background(), workspaceTransferProgressKey{}, func(n int) { count += n })
	body := trackWorkspaceBody(ctx, io.NopCloser(strings.NewReader("abcdef")))
	first := make([]byte, 2)
	if _, err := io.ReadFull(body, first); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("progress waited for the entire body")
	}
	rest, err := io.ReadAll(body)
	if err != nil || string(first)+string(rest) != "abcdef" || count != 6 {
		t.Fatal("progress altered content")
	}
	body.Close()
}
