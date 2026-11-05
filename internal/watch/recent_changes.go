package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func operationSnapshotPath(root, path string, owner Owner, tool string) string {
	return filepath.Join(root, "operation-snapshots", Key(path+"\x00"+owner.Session+"\x00"+owner.Turn+"\x00"+tool)+".json")
}
func reviewBaselinePath(root, path string, owner Owner) string {
	return filepath.Join(root, "review-baselines", Key(path), Key(owner.Session+"\x00"+owner.Turn)+".json")
}

func saveOperationSnapshot(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	// Abandoned hooks must not accumulate source snapshots indefinitely.
	entries, _ := os.ReadDir(filepath.Dir(file))
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(filepath.Join(filepath.Dir(file), entry.Name()))
		}
	}
	temp, err := os.CreateTemp(filepath.Dir(file), ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err = temp.Chmod(0600); err == nil {
		_, err = temp.Write(data)
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp.Name(), file)
}

var errCaptureEditScope = errors.New("could not capture the agent edit scope")

func recentAgentDiff(ctx context.Context, file, beforeDigest string, after sourcesnapshot.Capture) (diff string, err error) {
	defer func() {
		// A hook deadline is not a completed capture. Keep the immutable
		// pre-edit snapshot so the watcher can recover the exact edit later.
		if err == nil {
			_ = os.Remove(file)
		}
	}()
	return snapshotAgentDiff(ctx, file, beforeDigest, after)
}

// A turn baseline is retained across edits; per-operation snapshots are disposable.
func snapshotAgentDiff(ctx context.Context, file, beforeDigest string, after sourcesnapshot.Capture) (string, error) {
	current, err := sourcesnapshot.DecodeWorkspace(after.Bytes)
	if err != nil {
		return "", fmt.Errorf("%w: decode current snapshot: %v", errCaptureEditScope, err)
	}
	var before sourcesnapshot.Manifest
	if beforeDigest == "clean" {
		before.Target = current.Base
		before.Blobs = current.Blobs
	} else {
		info, err := os.Stat(file)
		if err != nil {
			return "", fmt.Errorf("%w: read baseline metadata: %v", errCaptureEditScope, err)
		}
		if info.Size() > sourcesnapshot.MaxEncodedWorkspaceBytes {
			return "", fmt.Errorf("%w: baseline exceeds the snapshot size limit", errCaptureEditScope)
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("%w: read baseline: %v", errCaptureEditScope, err)
		}
		if sourcesnapshot.Digest(raw) != beforeDigest {
			return "", fmt.Errorf("%w: baseline digest mismatch", errCaptureEditScope)
		}
		before, err = sourcesnapshot.DecodeWorkspace(raw)
		if err != nil {
			return "", fmt.Errorf("%w: decode baseline: %v", errCaptureEditScope, err)
		}
	}
	diff, err := Diff(ctx, changedTrees(before, current))
	if err != nil {
		return "", fmt.Errorf("%w: generate diff: %w", errCaptureEditScope, err)
	}
	if len(diff) > 128<<10 {
		return "", fmt.Errorf("%w: edit patch exceeds 128 KiB", errCaptureEditScope)
	}
	return diff, nil
}
func changedTrees(before, after sourcesnapshot.Manifest) sourcesnapshot.Manifest {
	old, newEntries := map[string]sourcesnapshot.Entry{}, map[string]sourcesnapshot.Entry{}
	for _, e := range before.Target {
		old[e.Path] = e
	}
	for _, e := range after.Target {
		newEntries[e.Path] = e
	}
	result := sourcesnapshot.Manifest{Blobs: map[string][]byte{}}
	for _, e := range before.Target {
		if n, ok := newEntries[e.Path]; !ok || n != e {
			result.Base = append(result.Base, e)
			result.Blobs[e.Hash] = before.Blobs[e.Hash]
		}
	}
	for _, e := range after.Target {
		if p, ok := old[e.Path]; !ok || p != e {
			result.Target = append(result.Target, e)
			result.Blobs[e.Hash] = after.Blobs[e.Hash]
		}
	}
	return result
}

func capturedAgentDiff(ctx context.Context, root, path string, state State, owner Owner, toolID, before string, capture sourcesnapshot.Capture) (string, error) {
	if state.DiffOwner == owner && state.DiffBase != "" {
		return snapshotAgentDiff(ctx, reviewBaselinePath(root, path, owner), state.DiffBase, capture)
	}
	// Operations recorded by an older runtime have only their own baseline.
	return recentAgentDiff(ctx, operationSnapshotPath(root, path, owner, toolID), before, capture)
}
