package livewatch

import (
	"context"
	"encoding/json"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"strings"
	"testing"
	"time"
)

func TestQuickEvidenceExcludesLargeUnrelatedSource(t *testing.T) {
	small := []byte("package test\nfunc Changed() {}\n")
	big := []byte(strings.Repeat("x", 300<<10))
	sh, bh := sourcesnapshot.Digest(small), sourcesnapshot.Digest(big)
	m := sourcesnapshot.Manifest{Target: []sourcesnapshot.Entry{{Path: "big.txt", Hash: bh, Mode: "100644"}, {Path: "small.go", Hash: sh, Mode: "100644"}}, Blobs: map[string][]byte{sh: small, bh: big}}
	got := quickEvidence(m, []adversarylabs.WorkspaceHypothesis{{Path: "small.go", Line: 2}})
	if len(got.Target) != 1 || got.Target[0].Path != "small.go" || len(got.Blobs) != 1 {
		t.Fatalf("unbounded packet: %+v", got.Target)
	}
	if len(m.Target) != 2 {
		t.Fatal("mutated full snapshot")
	}
}
func TestQuickEvidenceDoesNotWaitForBackgroundUpload(t *testing.T) {
	client := &fakeClient{}
	m := sourcesnapshot.Manifest{Blobs: map[string][]byte{}}
	_, _, _, err := uploadEvidence(context.Background(), client, Config{}, "repo", m, nil)
	if err != nil {
		t.Fatal(err)
	}
}

type blockedFullUploadClient struct {
	fakeClient
	release chan struct{}
	blocked chan struct{}
}

func (c *blockedFullUploadClient) UploadWorkspaceTree(ctx context.Context, token, repo string, entries []sourcesnapshot.Entry, blobs map[string][]byte) (string, string, error) {
	for _, e := range entries {
		if e.Path == "large.txt" {
			select {
			case c.blocked <- struct{}{}:
			default:
			}
			select {
			case <-c.release:
			case <-ctx.Done():
				return "", "", ctx.Err()
			}
			break
		}
	}
	return "repo", "snapshot", nil
}
func TestQuickCompletesWhileFullUploadIsBlocked(t *testing.T) {
	root := t.TempDir()
	repo := makeRepository(t, root, "repo")
	big := []byte(strings.Repeat("x", 300<<10))
	small := []byte("after\n")
	before := []byte("before\n")
	m := sourcesnapshot.Manifest{Version: 1, Scope: "worktree", BaseRef: "base", HeadRef: "head", Base: []sourcesnapshot.Entry{{Path: "file.txt", Hash: sourcesnapshot.Digest(before), Mode: "100644"}}, Target: []sourcesnapshot.Entry{{Path: "file.txt", Hash: sourcesnapshot.Digest(small), Mode: "100644"}, {Path: "large.txt", Hash: sourcesnapshot.Digest(big), Mode: "100644"}}, Blobs: map[string][]byte{sourcesnapshot.Digest(before): before, sourcesnapshot.Digest(small): small, sourcesnapshot.Digest(big): big}}
	raw, _ := json.Marshal(m)
	snapshot := sourcesnapshot.Capture{Bytes: raw}
	client := &blockedFullUploadClient{release: make(chan struct{}), blocked: make(chan struct{}, 1)}
	w := &worker{cfg: Config{}, client: client, allowed: func(context.Context, string) bool { return true }, prepareSlots: make(chan struct{}, 1)}
	w.prepareInBackground(repo, snapshot)
	select {
	case <-client.blocked:
	case <-time.After(time.Second):
		t.Fatal("background upload not started")
	}
	cfg := Config{RequireHypotheses: true, hypotheses: func(context.Context, string, string, int) ([]adversarylabs.WorkspaceHypothesis, error) {
		return []adversarylabs.WorkspaceHypothesis{}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := review(ctx, client, cfg, repo, snapshot, 1)
	close(client.release)
	w.background.Wait()
	if result.Status != "ready" {
		t.Fatalf("Quick waited for full upload: %+v", result)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.inputs) != 1 {
		t.Fatalf("submissions: %d", len(client.inputs))
	}
	input := client.inputs[0]
	if input.FullReview != nil || !input.EvidenceContext || input.FullSnapshotID != adversarylabs.WorkspaceSnapshotID(sourcesnapshot.LocalRepositoryIdentifier(repo), m.Target, m.Blobs) || input.FullBaseSnapshotID != adversarylabs.WorkspaceSnapshotID(sourcesnapshot.LocalRepositoryIdentifier(repo), m.Base, m.Blobs) {
		t.Fatalf("missing automatic Deep handoff: %+v", input)
	}
}

func TestLatestEditExcludesExistingDirtyBranchChanges(t *testing.T) {
	old := []byte("old\n")
	branch := []byte("existing branch change\n")
	latest := []byte("existing branch change\n// comment\n")
	entry := func(path string, b []byte) sourcesnapshot.Entry {
		return sourcesnapshot.Entry{Path: path, Hash: sourcesnapshot.Digest(b), Mode: "100644"}
	}
	m := sourcesnapshot.Manifest{Base: []sourcesnapshot.Entry{entry("a.go", old), entry("b.go", old)}, Target: []sourcesnapshot.Entry{entry("a.go", latest), entry("b.go", branch)}, Blobs: map[string][]byte{sourcesnapshot.Digest(old): old, sourcesnapshot.Digest(branch): branch, sourcesnapshot.Digest(latest): latest}}
	diff, err := editScope(context.Background(), m, map[string]initialFile{"a.go": {body: branch, mode: "100644"}, "b.go": {body: branch, mode: "100644"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(diff, "b.go") || !strings.Contains(diff, "+// comment") {
		t.Fatalf("scope re-reviewed old changes: %s", diff)
	}
}
