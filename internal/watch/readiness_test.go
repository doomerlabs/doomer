package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestHookReadinessCachesFailuresAndRecoversForSelectedProfile(t *testing.T) {
	var calls atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/healthz" || r.Header.Get("Authorization") != "" {
			t.Error("health probe sent the wrong path or credentials")
		}
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	root := t.TempDir()
	cfg := Config{API: server.URL + "/api", Profile: "local", AccountKey: "one"}
	if hookServiceReady(context.Background(), root, cfg) {
		t.Fatal("unavailable service accepted")
	}
	healthy.Store(true)
	if hookServiceReady(context.Background(), root, cfg) || calls.Load() != 1 {
		t.Fatal("failure cooldown did not release the hook without another probe")
	}
	state := hookServiceState{API: cfg.API, Profile: cfg.Profile, AccountKey: cfg.AccountKey, CheckedAt: time.Now().Add(-hookFailureCooldown - time.Second)}
	if err := Write(filepath.Join(root, "hook-service.json"), state); err != nil {
		t.Fatal(err)
	}
	if !hookServiceReady(context.Background(), root, cfg) || calls.Load() != 2 {
		t.Fatal("expired cooldown did not recover")
	}
	if !hookServiceReady(context.Background(), root, cfg) || calls.Load() != 2 {
		t.Fatal("healthy probe was not cached")
	}
	cfg.Profile = "production"
	if !hookServiceReady(context.Background(), root, cfg) || calls.Load() != 3 {
		t.Fatal("readiness crossed profile boundaries")
	}
	cfg.AccountKey = "other-account"
	if !hookServiceReady(context.Background(), root, cfg) || calls.Load() != 4 {
		t.Fatal("readiness crossed account boundaries")
	}
}

func TestHookPingIsBoundedAndDoesNotFollowRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	started := time.Now()
	if pingHookService(context.Background(), server.URL+"/api") || time.Since(started) > 2*time.Second {
		t.Fatal("health probe did not fail within its short timeout")
	}
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	if pingHookService(context.Background(), redirect.URL+"/api") || followed.Load() != 0 {
		t.Fatal("health probe followed a redirect")
	}
	for _, body := range []string{`{"ok":false}`, `<html>Login</html>`} {
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		if pingHookService(context.Background(), bad.URL+"/api") {
			t.Error("invalid health response accepted")
		}
		bad.Close()
	}
}

func TestOfflineStopReturnsEmptyBeforeCapturingRepository(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	owner := Owner{"chat", "turn"}
	cfg := Config{Directories: []string{dir}}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner})
	Write(StatePath(root, dir), State{Owner: owner, Fingerprint: "unchanged-sentinel"})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: "Stop"})
	var output bytes.Buffer
	var checks int
	hookWithReadiness(context.Background(), root, cfg, bytes.NewReader(raw), &output, func(context.Context) bool { checks++; return false })
	var state State
	Read(StatePath(root, dir), &state)
	if checks != 1 || strings.TrimSpace(output.String()) != "{}" || state.Fingerprint != "unchanged-sentinel" {
		t.Fatalf("offline hook waited, captured source, or produced feedback: %d %s %+v", checks, output.String(), state)
	}
}

func TestWaitingHookReleasesOnFailedReviewFeed(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	owner := Owner{"chat", "turn"}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := snapshotDigest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner})
	Write(StatePath(root, dir), State{Owner: owner, Fingerprint: fingerprint, HypothesesSubmitted: true, RequestedAt: time.Now()})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: "Stop"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var output bytes.Buffer
	checks := 0
	hookWithReadiness(ctx, root, Config{Directories: []string{dir}}, bytes.NewReader(raw), &output, func(context.Context) bool {
		checks++
		if checks == 2 {
			Write(FeedPath(root, dir), Feed{Owner: owner, Fingerprint: fingerprint, AssessmentStatus: "unavailable", Updated: time.Now()})
		}
		return true
	})
	if ctx.Err() != nil || strings.TrimSpace(output.String()) != "{}" {
		t.Fatal("failed review did not release hook quietly", output.String())
	}
}

func TestHookWaitTimeoutStartsCooldown(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	owner := Owner{"chat", "turn"}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := snapshotDigest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{API: "https://example.com/api", Profile: "local", Directories: []string{dir}}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner})
	Write(StatePath(root, dir), State{Owner: owner, Fingerprint: fingerprint, HypothesesSubmitted: true, RequestedAt: time.Now()})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: "Stop"})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var output bytes.Buffer
	hookWithReadiness(ctx, root, cfg, bytes.NewReader(raw), &output, func(context.Context) bool { return true })
	ready, cached := cachedHookReadiness(root, cfg)
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) || !cached || ready || strings.TrimSpace(output.String()) != "{}" {
		t.Fatal("timed out hook did not release silently and enter cooldown", output.String())
	}
}

type failedUploadClient struct{ fakeClient }

func (failedUploadClient) UploadWorkspaceTree(context.Context, string, string, []sourcesnapshot.Entry, map[string][]byte) (string, string, error) {
	return "", "", errors.New("service unavailable")
}

func TestFailedUploadWritesOwnedUnavailableFeed(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	capture, err := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Workspace: true, Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	owner := Owner{"chat", "turn"}
	Write(StatePath(root, dir), State{Owner: owner, Fingerprint: capture.Digest})
	runner := Runner{Root: root, Client: &failedUploadClient{}, Output: io.Discard}
	if err := runner.review(context.Background(), dir, capture); err == nil {
		t.Fatal("failed upload succeeded")
	}
	var feed Feed
	if err := Read(FeedPath(root, dir), &feed); err != nil || feed.Owner != owner || feed.Fingerprint != capture.Digest || feed.AssessmentStatus != "unavailable" || len(feed.Leads) != 0 {
		t.Fatal("failure did not release the owning hook", err, feed)
	}
}
