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
	if err := Read(runner.uploadCachePath(path), &cache); err != nil {
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
	if _, _, _, err = runner.UploadSnapshots(context.Background(), path, "org/repo", decoded); err != nil {
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
