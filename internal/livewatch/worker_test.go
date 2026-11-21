package livewatch

import (
	"context"
	"fmt"

	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"github.com/fsnotify/fsnotify"
)

func TestHookWatcherPollsSparinglyAndExpiresWithoutControl(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var scans atomic.Int32
		events := make(chan fsnotify.Event)
		w := &worker{
			cfg:      Config{Agent: "codex", RequireHypotheses: true, IdleTimeout: time.Minute, ControlTimeout: 2 * time.Minute},
			watcher:  &fsnotify.Watcher{Events: events},
			allowed:  func(context.Context, string) bool { scans.Add(1); return true },
			controls: make(chan control),
		}
		done := make(chan error, 1)
		go func() { done <- w.run(context.Background()) }()

		time.Sleep(2 * time.Second)
		synctest.Wait()
		if got := scans.Load(); got != 0 {
			t.Fatalf("idle hook watcher scanned within two seconds: %d", got)
		}
		time.Sleep(29 * time.Second)
		synctest.Wait()
		if got := scans.Load(); got != 1 {
			t.Fatalf("expected a fallback scan at 30 seconds, got %d", got)
		}
		// Renew the idle deadline at 31 seconds.
		sent := make(chan struct{})
		close(sent)
		reply := make(chan Response, 1)
		w.controls <- control{results: true, reply: reply, sent: sent}
		<-reply

		time.Sleep(58 * time.Second)
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("watcher expired before its renewed deadline: %v", err)
		default:
		}
		before := scans.Load()
		events <- fsnotify.Event{Op: fsnotify.Write}
		time.Sleep(800 * time.Millisecond)
		synctest.Wait()
		if got := scans.Load() - before; got != 1 {
			t.Fatalf("expected an event-driven scan before the next fallback, got %d", got)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("watcher survived its renewed idle deadline")
		}
	})
}

func TestHookWatcherSourceChangesExtendIdleTimeUntilControlDeadline(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	cfg := Config{
		Directory:         repo,
		Agent:             "codex",
		RequireHypotheses: true,
		IdleTimeout:       3 * time.Second,
		ControlTimeout:    5 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	baseline := make(chan struct{})
	var discovered sync.Once
	allowed := func(_ context.Context, path string) bool {
		if path != "" {
			discovered.Do(func() { close(baseline) })
		}
		return true
	}
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, &fakeClient{}, allowed) }()
	select {
	case <-baseline:
	case err := <-done:
		t.Fatalf("watcher failed during startup: %v", err)
	case <-ctx.Done():
		t.Fatal("watcher did not scan the repository")
	}

	started := time.Now()
	edits := time.NewTicker(time.Second)
	defer edits.Stop()
	overdue := time.NewTimer(7 * time.Second)
	defer overdue.Stop()
	for {
		select {
		case edit := <-edits.C:
			if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte(edit.String()), 0600); err != nil {
				t.Fatal(err)
			}
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(started); elapsed < cfg.ControlTimeout-500*time.Millisecond {
				t.Fatalf("watcher expired after %v despite ongoing edits", elapsed)
			}
			return
		case <-overdue.C:
			t.Fatal("ongoing edits extended the watcher past the control deadline")
		}
	}
}

type fakeClient struct {
	mu      sync.Mutex
	inputs  []adversarylabs.WorkspaceSubmission
	uploads int
	started chan adversarylabs.WorkspaceSubmission
	block   bool
	release <-chan struct{}
}

func (f *fakeClient) UploadWorkspaceTree(context.Context, string, string, []sourcesnapshot.Entry, map[string][]byte) (string, string, error) {
	f.mu.Lock()
	f.uploads++
	f.mu.Unlock()
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
func (f *fakeClient) ReportWorkspaceLead(context.Context, string, string, string, string, string, *bool) error {
	return nil
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

func startWorker(t *testing.T, directory string, client Client, drain time.Duration, allowed func(context.Context, string) bool, identity ...string) (Config, <-chan error) {
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
	if input.FullReview != nil || input.UserRequest != nil {
		t.Fatal("standalone watch invented agent context or overrode automatic Deep eligibility")
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
	client := &fakeClient{started: make(chan adversarylabs.WorkspaceSubmission, 10)}
	namespace := t.TempDir()
	for _, turn := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("before this turn\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, done := startWorker(t, repo, client, time.Second, func(context.Context, string) bool { return true }, namespace+turn)
		if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("same edit\n"), 0600); err != nil {
			t.Fatal(err)
		}
		stopWorker(t, cfg, done)
	}
	first, second := <-client.started, <-client.started
	if first.Diff != second.Diff || first.ReviewID == second.ReviewID {
		t.Fatal("distinct watches were deduplicated")
	}
}

func TestStopDeadlineCancelsPendingWork(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")

	client := &fakeClient{block: true, started: make(chan adversarylabs.WorkspaceSubmission, 1)}
	cfg, done := startWorker(t, repo, client, 50*time.Millisecond, func(context.Context, string) bool { return true })
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := stopWorker(t, cfg, done)
	if len(result.Results) != 1 || result.Results[0].Status != "unavailable" {
		t.Fatalf("expected explicit timeout: %+v", result)
	}
}

func TestStopDrainsLatestEditBeforeWorkerExits(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")

	release := make(chan struct{})
	client := &fakeClient{release: release, started: make(chan adversarylabs.WorkspaceSubmission, 5)}
	cfg, done := startWorker(t, repo, client, 2*time.Second, func(context.Context, string) bool { return true })
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("first edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
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
	if err != nil || len(paths) != 2 || paths[0] != parent || paths[1] != inside {
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

type publicLinkClient struct{ fakeClient }

func (f *publicLinkClient) StartWorkspace(context.Context, string, adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	return adversarylabs.WorkspaceReview{ID: "review", Status: "queued", URL: "https://dev.example.exe.xyz/look-closer/org/repo/" + strings.Repeat("a", 64)}, nil
}
func (f *publicLinkClient) GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error) {
	return adversarylabs.WorkspaceReview{ID: "review", Status: "completed", Early: &adversarylabs.WorkspaceAssessment{Status: "ready"}}, nil
}
func TestReviewRetainsPublicLinkWhenPollingOmitsIt(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := capture(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	result := review(context.Background(), &publicLinkClient{}, Config{ID: "watch", API: "http://localhost:3000/api", Token: "test"}, repo, snapshot, 1)
	if result.Status != "ready" || result.URL != "https://dev.example.exe.xyz/look-closer/org/repo/"+strings.Repeat("a", 64) {
		t.Fatalf("review link lost: %+v", result)
	}
}

func TestWatchStartupDoesNotUploadExistingChanges(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(fmt.Sprintf("dirty=%t", dirty), func(t *testing.T) {
			repo := makeRepository(t, t.TempDir(), "repo")
			if dirty {
				if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("existing edit\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("existing new file\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			client := &fakeClient{}
			for turn := 0; turn < 2; turn++ {
				cfg, done := startWorker(t, repo, client, time.Second, func(context.Context, string) bool { return true })
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				checkpoint, err := Call(ctx, cfg.ControlKey, "checkpoint")
				cancel()
				if err != nil || len(checkpoint.Results) != 0 {
					t.Fatalf("unchanged checkpoint: %+v %v", checkpoint, err)
				}
				result := stopWorker(t, cfg, done)
				if len(result.Results) != 0 {
					t.Fatalf("unchanged stop: %+v", result)
				}
			}
			client.mu.Lock()
			defer client.mu.Unlock()
			if client.uploads != 0 || len(client.inputs) != 0 {
				t.Fatalf("startup sent %d uploads and %d reviews", client.uploads, len(client.inputs))
			}
		})
	}
}

func TestDirtyWatchReviewsAfterNewFileChange(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	file := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(file, []byte("existing edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	cfg, done := startWorker(t, repo, client, time.Second, func(context.Context, string) bool { return true })
	if err := os.WriteFile(file, []byte("changed after startup\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result := stopWorker(t, cfg, done)
	if len(result.Results) != 1 || result.Results[0].Status != "ready" {
		t.Fatalf("missing post-start review: %+v", result)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.uploads == 0 || len(client.inputs) != 1 || !strings.Contains(client.inputs[0].Diff, "changed after startup") {
		t.Fatalf("wrong post-start submission: uploads=%d reviews=%+v", client.uploads, client.inputs)
	}
	if input := client.inputs[0]; input.ReviewScope != "diff" || input.AgentDiff != input.Diff {
		t.Fatalf("explicit diff review changed its inline diff payload: %+v", input)
	}
}

func TestAgentHypothesesGateSubmissionAndBindSnapshot(t *testing.T) {
	root := t.TempDir()
	repo := makeRepository(t, root, "repo")
	client := &fakeClient{}
	cfg := Config{ID: Key(t.TempDir()), ControlKey: Key(t.TempDir()), Directory: root, API: "http://localhost/api", Token: "test", Agent: "codex", RequireHypotheses: true}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, client, func(context.Context, string) bool { return true }) }()
	for {
		if _, ok := Available(ctx, cfg.ControlKey); ok {
			break
		}
		select {
		case err := <-done:
			t.Fatal(err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var tasks []HypothesisRequest
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		r, err := Call(ctx, cfg.ControlKey, "checkpoint")
		if err != nil {
			t.Fatal(err)
		}
		if len(r.HypothesisRequests) > 0 {
			tasks = r.HypothesisRequests
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(tasks) != 1 || !strings.Contains(tasks[0].Diff, "+after") {
		t.Fatalf("missing captured request: %+v", tasks)
	}
	client.mu.Lock()
	calls := len(client.inputs)
	client.mu.Unlock()
	if calls != 0 {
		t.Fatal("submitted before agent assessment")
	}
	if _, err := SubmitHypotheses(ctx, cfg.ControlKey, HypothesisResponse{RequestID: "stale", Hypotheses: []adversarylabs.WorkspaceHypothesis{}}); err == nil {
		t.Fatal("accepted stale request")
	}
	if _, err := SubmitHypotheses(ctx, cfg.ControlKey, HypothesisResponse{RequestID: tasks[0].ID}); err == nil {
		t.Fatal("accepted missing array")
	}
	if _, err := SubmitHypotheses(ctx, cfg.ControlKey, HypothesisResponse{RequestID: tasks[0].ID, Hypotheses: []adversarylabs.WorkspaceHypothesis{}}); err != nil {
		t.Fatal(err)
	}
	result, err := Call(ctx, cfg.ControlKey, "checkpoint")
	if err != nil || len(result.Results) != 1 || result.Results[0].Status != "ready" {
		t.Fatalf("completion: %+v %v", result, err)
	}
	if _, err := SubmitHypotheses(ctx, cfg.ControlKey, HypothesisResponse{RequestID: tasks[0].ID, Hypotheses: []adversarylabs.WorkspaceHypothesis{}}); err == nil {
		t.Fatal("accepted replay")
	}

	if err := os.WriteFile(filepath.Join(repo, "later.txt"), []byte("edit after checkpoint\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = Call(ctx, cfg.ControlKey, "finish")
	if err != nil || len(result.HypothesisRequests) != 1 {
		t.Fatalf("Stop did not hand off the later edit: %+v, %v", result, err)
	}
	later := result.HypothesisRequests[0]
	if later.ID == tasks[0].ID || later.Revision != tasks[0].Revision+1 || !strings.Contains(later.Diff, "+edit after checkpoint\n") {
		t.Fatalf("Stop handed off the wrong snapshot: %+v", later)
	}
	if _, err := SubmitHypotheses(ctx, cfg.ControlKey, HypothesisResponse{RequestID: later.ID, Hypotheses: []adversarylabs.WorkspaceHypothesis{}}); err != nil {
		t.Fatal(err)
	}
	result, err = Call(ctx, cfg.ControlKey, "finish")
	if err != nil || len(result.Results) != 1 || result.Results[0].ID != later.ReviewID || result.Results[0].Status != "ready" {
		t.Fatalf("Stop did not complete the later review: %+v, %v", result, err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.inputs) != 2 || !client.inputs[0].AgentAssessmentComplete || !client.inputs[1].AgentAssessmentComplete {
		t.Fatalf("expected two explicitly assessed review submissions, got %+v", client.inputs)
	}
}

func TestAgentWatchRevertToCleanCompletesWithoutReviewError(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	file := filepath.Join(repo, "file.txt")
	if err := os.WriteFile(file, []byte("prior prompt edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	w := &worker{
		cfg: Config{RequireHypotheses: true}, client: client,
		allowed: func(context.Context, string) bool { return true },
		repos:   map[string]*repository{repo: {pending: true}},
		slots:   make(chan struct{}, 1), completed: make(chan completion, 1),
	}
	// The next prompt undoes the previous prompt's edits back to HEAD.
	if err := os.WriteFile(file, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w.schedule(t.Context())
	select {
	case got := <-w.completed:
		if got.result.Status != "unchanged" || got.result.Error != "" {
			t.Fatalf("clean revert failed: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("clean revert did not complete")
	}
	w.jobs.Wait()
	if client.uploads != 0 || len(client.inputs) != 0 {
		t.Fatal("clean revert uploaded or submitted a review")
	}
}

func TestTurnEndStopsObservingLaterEdits(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	file := filepath.Join(repo, "file.txt")
	cfg := Config{ID: Key(t.TempDir()), ControlKey: Key(t.TempDir()), Directory: repo, API: "http://localhost/api", Token: "test", RequireHypotheses: true}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, &fakeClient{}, func(context.Context, string) bool { return true }) }()
	for {
		if _, ok := Available(ctx, cfg.ControlKey); ok {
			break
		}
		select {
		case err := <-done:
			t.Fatal(err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := os.WriteFile(file, []byte("turn edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := Call(ctx, cfg.ControlKey, "finish")
	if err != nil || len(first.HypothesisRequests) != 1 {
		t.Fatalf("finish: %+v %v", first, err)
	}
	request := first.HypothesisRequests[0]
	if err := os.WriteFile(file, []byte("later turn edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	current, err := Call(ctx, cfg.ControlKey, "results")
	if err != nil || len(current.HypothesisRequests) != 1 || current.HypothesisRequests[0].ID != request.ID {
		t.Fatalf("observed later edit: %+v %v", current, err)
	}
	if _, err := SubmitHypotheses(ctx, cfg.ControlKey, HypothesisResponse{RequestID: request.ID, Hypotheses: []adversarylabs.WorkspaceHypothesis{}}); err != nil {
		t.Fatal(err)
	}
	// Final Stop closes the watcher, regardless of the review's terminal outcome.
	final, err := Call(ctx, cfg.ControlKey, "finish")
	if err != nil {
		t.Fatal(err)
	}
	// The explicit final capture can discover a new edit; assess it first.
	for _, pending := range final.HypothesisRequests {
		if _, err := SubmitHypotheses(ctx, cfg.ControlKey, HypothesisResponse{RequestID: pending.ID, Hypotheses: []adversarylabs.WorkspaceHypothesis{}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(final.HypothesisRequests) > 0 {
		if _, err := Call(ctx, cfg.ControlKey, "finish"); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher survived final Stop")
	}
}

type expiredAssessmentClient struct {
	fakeClient
	editPath string
	expired  bool
}

func (c *expiredAssessmentClient) StartWorkspace(ctx context.Context, token string, input adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	if !c.expired {
		c.expired = true
		if err := os.WriteFile(c.editPath, []byte("latest edit\n"), 0600); err != nil {
			return adversarylabs.WorkspaceReview{}, err
		}
		return adversarylabs.WorkspaceReview{}, adversarylabs.ErrAgentAssessmentExpired
	}
	return c.fakeClient.StartWorkspace(ctx, token, input)
}

func TestExpiredAssessmentRetriesLatestEditOnce(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix IPC")
	}
	repo := makeRepository(t, t.TempDir(), "repo")
	file := filepath.Join(repo, "file.txt")
	cfg := Config{ID: Key(t.TempDir()), ControlKey: Key(t.TempDir()), Directory: repo, Agent: "codex", RequireHypotheses: true}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	client := &expiredAssessmentClient{editPath: file}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Serve(ctx, cfg, client, func(context.Context, string) bool { return true })
	}()
	defer func() { cancel(); <-done }()
	for {
		if _, ok := Available(ctx, cfg.ControlKey); ok {
			break
		}
		select {
		case <-done:
			t.Fatal("watcher exited during startup")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := os.WriteFile(file, []byte("first edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	assess := func() HypothesisRequest {
		t.Helper()
		report, err := Call(ctx, cfg.ControlKey, "checkpoint")
		if err != nil || len(report.HypothesisRequests) != 1 {
			t.Fatalf("wanted one assessment: %+v %v", report, err)
		}
		task := report.HypothesisRequests[0]
		if _, err := SubmitHypotheses(ctx, cfg.ControlKey, HypothesisResponse{RequestID: task.ID, Hypotheses: []adversarylabs.WorkspaceHypothesis{}}); err != nil {
			t.Fatal(err)
		}
		return task
	}
	initial, retry := assess(), assess()
	if retry.ReviewID == initial.ReviewID || retry.ID == initial.ID || !strings.Contains(retry.Diff, "+latest edit") {
		t.Fatalf("retry did not request a fresh assessment of the latest edit: %+v", retry)
	}
	report, err := Call(ctx, cfg.ControlKey, "checkpoint")
	if err != nil || len(report.HypothesisRequests) != 0 || len(report.Results) != 1 || report.Results[0].ID != retry.ReviewID || report.Results[0].Status != "ready" {
		t.Fatalf("replacement did not finish without a duplicate review: %+v %v", report, err)
	}
}

func TestTerminalReportKeepsNewFindingsAvailable(t *testing.T) {
	repo := makeRepository(t, t.TempDir(), "repo")
	cfg, done := startWorker(t, repo, &terminalFindingClient{}, 0, func(context.Context, string) bool { return true }, Key(t.TempDir()))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := Call(ctx, cfg.ControlKey, "finish")
	if err != nil || len(report.Findings) != 1 {
		t.Fatalf("missing pending finding: %+v %v", report, err)
	}
	select {
	case err := <-done:
		t.Fatalf("closed with pending feedback: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	yes := false
	if _, err := SubmitFeedback(ctx, cfg.ControlKey, []Feedback{{ReviewID: report.Findings[0].ReviewID, LeadID: "lead", Status: "dismissed", Reason: "Verified fixture", Agrees: &yes}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Call(ctx, cfg.ControlKey, "finish"); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeFindingStopFollowupRequiresAnUnansweredHandoff(t *testing.T) {
	for _, tc := range []struct {
		name       string
		delivery   control
		resolved   bool
		denied     bool
		wantFollow bool
	}{
		{name: "no findings", delivery: control{checkpoint: true}},
		{name: "unseen finding", delivery: control{results: true}},
		{name: "checkpoint handoff", delivery: control{checkpoint: true}, wantFollow: true},
		{name: "explicit list handoff", delivery: control{listFindings: true}, wantFollow: true},
		{name: "answered after delivery", delivery: control{checkpoint: true}, resolved: true},
		{name: "denied after delivery", delivery: control{checkpoint: true}, denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := worker{findings: map[string]Finding{}}
			if tc.name != "no findings" {
				w.findings["review/lead"] = Finding{ReviewID: "review", Lead: adversarylabs.WorkspaceLead{ID: "lead"}}
			}
			report := w.response(false)
			// The report can contain a published finding and a later assessment.
			report.HypothesisRequests = []HypothesisRequest{{ID: "new-assessment"}}
			w.prepareClaudeFindingHandoff(tc.delivery, &report)
			wantHandoff := len(report.Findings) > 0 && !tc.delivery.results
			if report.FindingHandoff != wantHandoff {
				t.Fatalf("finding handoff = %v, want %v", report.FindingHandoff, wantHandoff)
			}
			if tc.resolved {
				finding := w.findings["review/lead"]
				finding.Disposition = &Feedback{Status: "dismissed"}
				w.findings["review/lead"] = finding
			}
			w.findingToolDenied = tc.denied
			stop := control{finish: true, checkpoint: true}
			w.prepareClaudeFindingHandoff(stop, &report)
			if report.FindingStopFollowup {
				t.Fatal("finding follow-up displaced the pending assessment")
			}
			for _, want := range []bool{tc.wantFollow, false} {
				report = w.response(false)
				w.prepareClaudeFindingHandoff(stop, &report)
				if report.FindingStopFollowup != want {
					t.Fatalf("finding follow-up = %v, want %v", report.FindingStopFollowup, want)
				}
			}
		})
	}
}

type terminalFindingClient struct{ fakeClient }

func (f *terminalFindingClient) StartWorkspace(_ context.Context, _ string, in adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	return adversarylabs.WorkspaceReview{ID: in.ReviewID, Status: "completed", Early: &adversarylabs.WorkspaceAssessment{Status: "ready", Leads: []adversarylabs.WorkspaceLead{{ID: "lead", Path: "file.txt", Line: 1}}}}, nil
}
