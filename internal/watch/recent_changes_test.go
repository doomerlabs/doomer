package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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

func TestTimedOutDiffRetainsSnapshotForWatcherRecovery(t *testing.T) {
	entry := func(body string) sourcesnapshot.Entry {
		return sourcesnapshot.Entry{Path: "a.go", Hash: sourcesnapshot.Digest([]byte(body)), Mode: "100644"}
	}
	old, next := entry("old\n"), entry("new\n")
	before := sourcesnapshot.Manifest{Version: 1, Scope: "worktree", Base: []sourcesnapshot.Entry{old}, Target: []sourcesnapshot.Entry{next}, Blobs: map[string][]byte{old.Hash: []byte("old\n"), next.Hash: []byte("new\n")}}
	raw, _ := json.Marshal(before)
	file := filepath.Join(t.TempDir(), "before.json")
	if err := saveOperationSnapshot(file, raw); err != nil {
		t.Fatal(err)
	}
	after := before
	after.Target = []sourcesnapshot.Entry{old}
	// Keep a valid changed target for decoding while canceling the diff process.
	after.Base = []sourcesnapshot.Entry{next}
	target, _ := json.Marshal(after)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := recentAgentDiff(ctx, file, sourcesnapshot.Digest(raw), sourcesnapshot.Capture{Bytes: target}); got != "" {
		t.Fatal("canceled diff unexpectedly succeeded")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("lost retry snapshot", err)
	}
	if got := recentAgentDiff(context.Background(), file, sourcesnapshot.Digest(raw), sourcesnapshot.Capture{Bytes: target}); !strings.Contains(got, "-new") || !strings.Contains(got, "+old") {
		t.Fatal("could not recover exact edit", got)
	}
}

func TestReplayedPreToolUseRetainsOriginalSnapshot(t *testing.T) {
	root, path := t.TempDir(), repository(t)
	if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("prior dirty work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	owner := Owner{"chat", "turn"}
	input := HookInput{Session: owner.Session, Turn: owner.Turn, CWD: path, Event: "PreToolUse", Tool: "apply_patch", ToolID: "invocation-1"}
	raw, _ := json.Marshal(input)
	cfg := Config{Directories: []string{path}}
	hookForTest(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard)
	snapshot := operationSnapshotPath(root, path, owner, input.ToolID)
	before, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "file.txt"), []byte("after edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deferCompletedEdit(root, path, owner, input.ToolID)
	hookForTest(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard)
	after, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("replayed event replaced original snapshot")
	}
	var state State
	if err := Read(StatePath(root, path), &state); err != nil {
		t.Fatal(err)
	}
	if state.Active[input.ToolID].CompletedAt.IsZero() {
		t.Fatal("replayed event reset pending recovery")
	}
}
