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
}
