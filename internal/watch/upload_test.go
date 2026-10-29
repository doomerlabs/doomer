package watch

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

type incrementalFake struct {
	fakeClient
	previous []string
}

func (f *incrementalFake) UploadWorkspaceIncremental(_ context.Context, _ string, _ string, entries []sourcesnapshot.Entry, _ map[string][]byte, previous *adversarylabs.WorkspaceTree, _ adversarylabs.WorkspaceUploadProgress) (adversarylabs.WorkspaceTree, error) {
	prior := ""
	if previous != nil {
		prior = previous.SnapshotID
	}
	f.previous = append(f.previous, prior)
	return adversarylabs.WorkspaceTree{RepositoryID: "repo", SnapshotID: fmt.Sprintf("s%d", len(f.previous)), Entries: entries}, nil
}
func TestUploadCacheReusesTreesButDoesNotCrossLogins(t *testing.T) {
	client := &incrementalFake{}
	runner := Runner{Root: t.TempDir(), Client: client, Token: "first-login", Output: io.Discard, Config: Config{API: "http://localhost:3000/api", Profile: "local"}}
	manifest := sourcesnapshot.Manifest{Base: []sourcesnapshot.Entry{}, Target: []sourcesnapshot.Entry{}, Blobs: map[string][]byte{}}
	for i := 0; i < 2; i++ {
		if _, _, _, err := runner.uploadSnapshots(context.Background(), "/repo", "org/repo", manifest); err != nil {
			t.Fatal(err)
		}
	}
	if got := fmt.Sprint(client.previous); got != "[ s1 s1 s2]" {
		t.Fatalf("prior snapshots: %s", got)
	}
	runner.Token = "second-login"
	if _, _, _, err := runner.uploadSnapshots(context.Background(), "/repo", "org/repo", manifest); err != nil {
		t.Fatal(err)
	}
	if client.previous[4] != "" || client.previous[5] != "s5" {
		t.Fatalf("login reused cache: %+v", client.previous)
	}
}
