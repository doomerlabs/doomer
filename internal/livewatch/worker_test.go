package livewatch

import (
	"context"

	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

type fakeClient struct {
	mu      sync.Mutex
	inputs  []adversarylabs.WorkspaceSubmission
	started chan adversarylabs.WorkspaceSubmission
	block   bool
	release <-chan struct{}
}

func (f *fakeClient) UploadWorkspaceTree(context.Context, string, string, []sourcesnapshot.Entry, map[string][]byte) (string, string, error) {
	return "repo", "snapshot", nil
}
func (f *fakeClient) StartWorkspace(ctx context.Context, _ string, in adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	f.mu.Lock()
	f.inputs = append(f.inputs, in)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- in
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return adversarylabs.WorkspaceReview{}, ctx.Err()
		}
	}
	if f.block {
		<-ctx.Done()
		return adversarylabs.WorkspaceReview{}, ctx.Err()
	}
	return adversarylabs.WorkspaceReview{ID: in.ReviewID, Status: "completed", Early: &adversarylabs.WorkspaceAssessment{Status: "ready"}}, nil
}
func (f *fakeClient) GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error) {
	panic("unexpected poll")
}

func makeRepository(t *testing.T, parent, relative string) string {
	t.Helper()
	path := filepath.Join(parent, relative)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", path}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "initial")
	return path
}

func startWorker(t *testing.T, directory string, client *fakeClient, drain time.Duration, allowed func(context.Context, string) bool, identity ...string) (Config, <-chan error) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix IPC")
	}
	cfg := Config{ID: t.TempDir(), ControlKey: Key(t.TempDir()), Directory: directory, API: "http://localhost:3000/api", Token: "test", DrainTimeout: drain}
	if len(identity) > 0 {
		cfg.ID = identity[0]
		cfg.ControlKey = Key(identity[0])
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, client, allowed) }()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if _, live := Available(ctx, cfg.ControlKey); live {
			return cfg, done
		}
		select {
		case err := <-done:
			t.Fatalf("startup: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not start")
	return cfg, done
}
func stopWorker(t *testing.T, cfg Config, done <-chan error) Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Call(ctx, cfg.ControlKey, "stop")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("worker did not exit")
	}
	return result
}

func TestRecursiveWatchCapturesOnlyChangedRepoAndExits(t *testing.T) {
	parent := t.TempDir()
	changed := makeRepository(t, parent, "a/b/c/d/changed")
	sibling := makeRepository(t, parent, "sibling")
	if err := os.WriteFile(filepath.Join(sibling, "file.txt"), []byte("old unrelated change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{started: make(chan adversarylabs.WorkspaceSubmission, 10)}
	cfg, done := startWorker(t, parent, client, time.Second, func(context.Context, string) bool { return true })
	if err := os.WriteFile(filepath.Join(changed, "file.txt"), []byte("during this watch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := stopWorker(t, cfg, done)
	if len(result.Results) != 1 || result.Results[0].Repository != changed || result.Results[0].Status != "ready" {
		t.Fatalf("unexpected results: %+v", result)
	}
	input := <-client.started
	if !strings.Contains(input.Diff, "during this watch") || strings.Contains(input.Diff, "old unrelated change") {
		t.Fatal(input.Diff)
	}
	if input.FullReview == nil || *input.FullReview || input.UserRequest != nil {
		t.Fatal("standalone watch invented agent context or enabled full review")
	}
	if _, live := Available(context.Background(), cfg.ControlKey); live {
		t.Fatal("watcher endpoint survived stop")
	}
}

func TestRepositoriesCreatedMidTurnAndApprovalBoundaries(t *testing.T) {
	parent := t.TempDir()
	client := &fakeClient{}
	allowed := func(_ context.Context, path string) bool { return path == "" || strings.HasSuffix(path, "allowed") }
	cfg, done := startWorker(t, parent, client, time.Second, allowed)
	for _, name := range []string{"deep/new/allowed", "deep/new/denied"} {
		p := makeRepository(t, parent, name)
		if err := os.WriteFile(filepath.Join(p, "file.txt"), []byte("new edit\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	result := stopWorker(t, cfg, done)
	if len(result.Results) != 1 || !strings.HasSuffix(result.Results[0].Repository, "allowed") {
		t.Fatalf("wrong repository selected: %+v", result)
	}
}

func TestSeparateInvocationsSubmitIdenticalContents(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("same edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{started: make(chan adversarylabs.WorkspaceSubmission, 10)}
	namespace := t.TempDir()
	for _, turn := range []string{"one", "two"} {
		cfg, done := startWorker(t, repo, client, time.Second, func(context.Context, string) bool { return true }, namespace+turn)
		stopWorker(t, cfg, done)
	}
	first, second := <-client.started, <-client.started
	if first.Diff != second.Diff || first.ReviewID == second.ReviewID {
		t.Fatal("distinct watches were deduplicated")
	}
}

func TestStopDeadlineCancelsPendingWork(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{block: true, started: make(chan adversarylabs.WorkspaceSubmission, 1)}
	cfg, done := startWorker(t, repo, client, 50*time.Millisecond, func(context.Context, string) bool { return true })
	result := stopWorker(t, cfg, done)
	if len(result.Results) != 1 || result.Results[0].Status != "unavailable" {
		t.Fatalf("expected explicit timeout: %+v", result)
	}
}

func TestStopDrainsLatestEditBeforeWorkerExits(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("first edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	client := &fakeClient{release: release, started: make(chan adversarylabs.WorkspaceSubmission, 5)}
	cfg, done := startWorker(t, repo, client, 2*time.Second, func(context.Context, string) bool { return true })
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("first review never started")
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("final edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := make(chan Response, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		out, _ := Call(ctx, cfg.ControlKey, "stop")
		result <- out
	}()
	close(release)
	select {
	case out := <-result:
		if len(out.Results) != 1 || out.Results[0].Status != "ready" {
			t.Fatalf("final result: %+v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop failed to drain")
	}
	select {
	case input := <-client.started:
		if !strings.Contains(input.Diff, "final edit") {
			t.Fatal(input.Diff)
		}
	case <-time.After(time.Second):
		t.Fatal("final edit was not submitted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDiffNeedsNoTemporaryFiles(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	old, new := []byte("one\ntwo\n"), []byte("one\nthree\n")
	m := sourcesnapshot.Manifest{Base: []sourcesnapshot.Entry{{Path: "file.txt", Mode: "100644", Hash: "old"}}, Target: []sourcesnapshot.Entry{{Path: "file.txt", Mode: "100644", Hash: "new"}}, Blobs: map[string][]byte{"old": old, "new": new}}
	diff, err := Diff(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "-two") || !strings.Contains(diff, "+three") {
		t.Fatal(diff)
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 0 {
		t.Fatal("diff wrote temporary files")
	}
}

func TestDiscoveryDoesNotFollowSymlinksOutsideWorkspace(t *testing.T) {
	parent := t.TempDir()
	inside := makeRepository(t, parent, "a/b/c/repository")
	outside := makeRepository(t, t.TempDir(), "outside")
	if err := os.Symlink(outside, filepath.Join(parent, "external")); err != nil {
		t.Skip(err)
	}
	paths, err := DiscoverRepositories(context.Background(), parent)
	if err != nil || len(paths) != 1 || paths[0] != inside {
		t.Fatalf("discovery escaped the workspace: %v %v", paths, err)
	}
}

func TestMemoryDiffPreservesMissingFinalNewline(t *testing.T) {
	m := sourcesnapshot.Manifest{Base: []sourcesnapshot.Entry{{Path: "file", Hash: "a", Mode: "100644"}}, Target: []sourcesnapshot.Entry{{Path: "file", Hash: "b", Mode: "100644"}}, Blobs: map[string][]byte{"a": []byte("before"), "b": []byte("after\n")}}
	diff, err := Diff(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "-before\n\\ No newline at end of file\n+after\n") {
		t.Fatal(diff)
	}
}

func TestSeparateInvocationsUseDistinctReviewIDs(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := capture(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	cfg := Config{API: "http://localhost:3000/api", Token: "test"}
	for _, turn := range []string{"one", "two"} {
		cfg.ID = turn
		if result := review(context.Background(), client, cfg, repo, snapshot, 1); result.Status != "ready" {
			t.Fatal(result)
		}
	}
	if client.inputs[0].ReviewID == client.inputs[1].ReviewID {
		t.Fatal("new watch reused a previous watch's review")
	}
}
