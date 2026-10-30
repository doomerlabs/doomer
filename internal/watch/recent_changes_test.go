package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestRecentAgentDiffUsesPreEditContentsNotGitBase(t *testing.T) {
	entry := func(data string) sourcesnapshot.Entry {
		return sourcesnapshot.Entry{Path: "new.go", Hash: sourcesnapshot.Digest([]byte(data)), Mode: "100644"}
	}
	original, before, after := entry("original\n"), entry("prior dirty work\n"), entry("new edit\n")
	old := sourcesnapshot.Manifest{Version: 1, Scope: "worktree", Base: []sourcesnapshot.Entry{original}, Target: []sourcesnapshot.Entry{before}, Blobs: map[string][]byte{original.Hash: []byte("original\n"), before.Hash: []byte("prior dirty work\n")}}
	next := sourcesnapshot.Manifest{Version: 1, Scope: "worktree", Base: []sourcesnapshot.Entry{original}, Target: []sourcesnapshot.Entry{after}, Blobs: map[string][]byte{original.Hash: []byte("original\n"), after.Hash: []byte("new edit\n")}}
	raw, _ := json.Marshal(old)
	target, _ := json.Marshal(next)
	file := filepath.Join(t.TempDir(), "before.json")
	if err := saveOperationSnapshot(file, raw); err != nil {
		t.Fatal(err)
	}
	diff := recentAgentDiff(context.Background(), file, sourcesnapshot.Digest(raw), sourcesnapshot.Capture{Bytes: target})
	if !strings.Contains(diff, "-prior dirty work") || !strings.Contains(diff, "+new edit") || strings.Contains(diff, "original") {
		t.Fatalf("wrong edit baseline: %s", diff)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("pre-edit snapshot was retained")
	}
}
