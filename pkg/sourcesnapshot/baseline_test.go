package sourcesnapshot

import (
	"context"
	"testing"
)

func TestBaselineAheadOfBaseContainsOnlyCommittedBase(t *testing.T) {
	dir, base := sourceRepo(t)
	sourceFile(t, dir, "head-only.go", "head content\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "head ahead")
	sourceFile(t, dir, "dirty-only.go", "dirty content\n")
	baseline, _, err := CaptureBaseline(context.Background(), CaptureOptions{Path: dir, Base: base})
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Target) != 0 {
		t.Fatal("baseline warmup should not contain a target")
	}
	for _, entry := range baseline.Base {
		if entry.Path == "head-only.go" || entry.Path == "dirty-only.go" {
			t.Fatal("warmup included content outside committed base")
		}
		if _, ok := baseline.Blobs[entry.Hash]; !ok {
			t.Fatal("base blob missing")
		}
	}
	capture, err := CaptureWorktree(context.Background(), CaptureOptions{Workspace: true, Path: dir, Base: base})
	if err != nil {
		t.Fatal(err)
	}
	full, err := DecodeWorkspace(capture.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, entry := range full.Target {
		found[entry.Path] = true
	}
	if !found["head-only.go"] || !found["dirty-only.go"] {
		t.Fatal("actual review omitted current contents")
	}
}
