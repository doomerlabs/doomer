package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestPromptPreparesCommittedBaselineOnlyAfterConsent(t *testing.T) {
	path, root := repository(t), t.TempDir()
	if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("uncommitted secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	owner := Owner{"chat", "prompt"}
	raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: path, Event: "UserPromptSubmit", Prompt: "change this"})
	client := &incrementalFake{}
	runner := Runner{Root: root, Config: Config{Directories: []string{path}}, Client: client}
	started := time.Now().Add(-time.Second)
	hookForTest(context.Background(), root, Config{}, bytes.NewReader(raw), io.Discard)
	var completed Owner
	runner.prepareBaseline(context.Background(), path, started, &completed)
	if len(client.previous) != 0 {
		t.Fatal("unapproved prompt uploaded")
	}
	hookForTest(context.Background(), root, runner.Config, bytes.NewReader(raw), io.Discard)
	runner.prepareBaseline(context.Background(), path, started, &completed)
	if len(client.previous) != 1 || client.starts != 0 {
		t.Fatal("baseline did not warm without starting a review")
	}
	var cache uploadCache
	if err := Read(runner.uploadCachePath(path, sourcesnapshot.LocalRepositoryIdentifier(path)), &cache); err != nil {
		t.Fatal(err)
	}
	if len(cache.Base.Entries) != 1 || cache.Base.Entries[0].Hash != sourcesnapshot.Digest([]byte("one\ntwo\n")) {
		t.Fatal("warmup captured dirty files instead of committed baseline")
	}
	runner.prepareBaseline(context.Background(), path, started, &completed)
	if len(client.previous) != 1 {
		t.Fatal("repeated baseline for the same turn")
	}
	manifest, err := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Workspace: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := sourcesnapshot.DecodeWorkspace(manifest.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = runner.UploadSnapshots(context.Background(), path, manifest.Source.Repository, decoded); err != nil {
		t.Fatal(err)
	}
	if client.previous[1] != "s1" {
		t.Fatal("review did not reuse pre-uploaded baseline")
	}
}

type failedHeadClient struct {
	incrementalFake
	fail bool
}

func (c *failedHeadClient) UploadWorkspaceIncremental(ctx context.Context, token, repo string, entries []sourcesnapshot.Entry, blobs map[string][]byte, previous *adversarylabs.WorkspaceTree, progress adversarylabs.WorkspaceUploadProgress) (adversarylabs.WorkspaceTree, error) {
	if len(c.previous) == 1 && !c.fail {
		c.fail = true
		return adversarylabs.WorkspaceTree{}, errors.New("head upload interrupted")
	}
	return c.incrementalFake.UploadWorkspaceIncremental(ctx, token, repo, entries, blobs, previous, progress)
}
func TestCompletedBaselineSurvivesHeadUploadFailure(t *testing.T) {
	c := &failedHeadClient{}
	r := Runner{Root: t.TempDir(), Client: c}
	m := sourcesnapshot.Manifest{Base: []sourcesnapshot.Entry{}, Target: []sourcesnapshot.Entry{}, Blobs: map[string][]byte{}}
	if _, _, _, err := r.UploadSnapshots(context.Background(), "/repo", "org/repo", m); err == nil {
		t.Fatal("expected failure")
	}
	if _, _, _, err := r.UploadSnapshots(context.Background(), "/repo", "org/repo", m); err != nil {
		t.Fatal(err)
	}
	if c.previous[1] != "s1" {
		t.Fatal("baseline lost after head upload failed", c.previous)
	}
}

type budgetClient struct {
	fakeClient
	uploadRemaining, analysisRemaining time.Duration
}

func (c *budgetClient) UploadWorkspaceTree(ctx context.Context, token, repo string, entries []sourcesnapshot.Entry, blobs map[string][]byte) (string, string, error) {
	deadline, ok := ctx.Deadline()
	if ok {
		c.uploadRemaining = time.Until(deadline)
	}
	return c.fakeClient.UploadWorkspaceTree(ctx, token, repo, entries, blobs)
}
func (c *budgetClient) StartWorkspace(ctx context.Context, token string, input adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	deadline, ok := ctx.Deadline()
	if ok {
		c.analysisRemaining = time.Until(deadline)
	}
	return c.fakeClient.StartWorkspace(ctx, token, input)
}
func TestUploadAndAssessmentUseSeparateDeadlines(t *testing.T) {
	path := repository(t)
	os.WriteFile(filepath.Join(path, "file.txt"), []byte("updated\n"), 0600)
	client := &budgetClient{}
	runner := Runner{Root: t.TempDir(), Client: client}
	requestTestAgentReview(t, &runner, path)
	capture, err := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Workspace: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.review(context.Background(), path, capture); err != nil {
		t.Fatal(err)
	}
	if client.uploadRemaining < 10*time.Minute {
		t.Fatal("initial upload inherited short review budget", client.uploadRemaining)
	}
	if client.analysisRemaining < 4*time.Minute || client.analysisRemaining > 5*time.Minute {
		t.Fatal("analysis did not get fresh bounded budget", client.analysisRemaining)
	}
}

type incrementalBudgetClient struct{ budgetClient }

func (c *incrementalBudgetClient) UploadWorkspaceIncremental(ctx context.Context, _ string, _ string, entries []sourcesnapshot.Entry, _ map[string][]byte, _ *adversarylabs.WorkspaceTree, _ adversarylabs.WorkspaceUploadProgress) (adversarylabs.WorkspaceTree, error) {
	deadline, _ := ctx.Deadline()
	c.uploadRemaining = time.Until(deadline)
	return adversarylabs.WorkspaceTree{RepositoryID: "repo", SnapshotID: "snapshot", Entries: entries}, nil
}
func TestIncrementalUploadAndAssessmentUseSeparateDeadlines(t *testing.T) {
	path := repository(t)
	if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("updated\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &incrementalBudgetClient{}
	runner := Runner{Root: t.TempDir(), Client: client}
	requestTestAgentReview(t, &runner, path)
	capture, err := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Workspace: true, Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.review(context.Background(), path, capture); err != nil {
		t.Fatal(err)
	}
	if client.uploadRemaining < 10*time.Minute {
		t.Fatal("short incremental upload budget", client.uploadRemaining)
	}
	if client.analysisRemaining < 4*time.Minute || client.analysisRemaining > 5*time.Minute {
		t.Fatal("invalid assessment budget", client.analysisRemaining)
	}
	owner := Owner{"warmup", "turn"}
	if err := Write(TurnPath(runner.Root, owner.Session), Turn{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	runner.Config.Directories = []string{path}
	requestBaseline(runner.Root, path, owner)
	var completed Owner
	client.uploadRemaining = 0
	runner.prepareBaseline(context.Background(), path, time.Now().Add(-time.Minute), &completed)
	if client.uploadRemaining < 10*time.Minute {
		t.Fatal("short baseline upload budget", client.uploadRemaining)
	}
}

// A warmup can overlap a separate explicit-review process, even though the
// watcher's own warmup and review loop is serial.
type overlappingBaselineClient struct {
	incrementalFake
	during func()
}

func (c *overlappingBaselineClient) UploadWorkspaceIncremental(ctx context.Context, token, repo string, entries []sourcesnapshot.Entry, blobs map[string][]byte, prior *adversarylabs.WorkspaceTree, progress adversarylabs.WorkspaceUploadProgress) (adversarylabs.WorkspaceTree, error) {
	c.during()
	return c.incrementalFake.UploadWorkspaceIncremental(ctx, token, repo, entries, blobs, prior, progress)
}
func TestBaselineCompletionRetainsConcurrentReviewHead(t *testing.T) {
	r := Runner{Root: t.TempDir()}
	path := r.uploadCachePath("/repo", "org/repo")
	c := &overlappingBaselineClient{during: func() {
		head := adversarylabs.WorkspaceTree{RepositoryID: "repo", SnapshotID: "new-head"}
		if err := mergeUploadCache(path, nil, &head); err != nil {
			t.Fatal(err)
		}
	}}
	r.Client = c
	if err := r.uploadBaseline(context.Background(), "/repo", "org/repo", sourcesnapshot.Manifest{Blobs: map[string][]byte{}}); err != nil {
		t.Fatal(err)
	}
	var cache uploadCache
	if err := Read(path, &cache); err != nil {
		t.Fatal(err)
	}
	if cache.Head.SnapshotID != "new-head" || cache.Base.SnapshotID == "" {
		t.Fatalf("lost completed uploads: %+v", cache)
	}
}

func TestAddingRemoteUsesSeparateUploadCache(t *testing.T) {
	client := &incrementalFake{}
	runner := Runner{Root: t.TempDir(), Client: client}
	manifest := sourcesnapshot.Manifest{Blobs: map[string][]byte{}}
	for _, repository := range []string{sourcesnapshot.LocalRepositoryIdentifier("/my-app"), "github.com/org/my-app"} {
		if _, _, _, err := runner.UploadSnapshots(context.Background(), "/my-app", repository, manifest); err != nil {
			t.Fatal(err)
		}
	}
	if len(client.previous) != 4 || client.previous[0] != "" || client.previous[2] != "" {
		t.Fatalf("remote review inherited the local snapshot cache: %v", client.previous)
	}
}
