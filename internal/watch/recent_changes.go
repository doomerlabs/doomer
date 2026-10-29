package watch

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func operationSnapshotPath(root, path string, owner Owner, tool string) string {
	return filepath.Join(root, "operation-snapshots", Key(path+"\x00"+owner.Session+"\x00"+owner.Turn+"\x00"+tool)+".json")
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
func recentAgentDiff(ctx context.Context, file, beforeDigest string, after sourcesnapshot.Capture) string {
	defer os.Remove(file)
	current, err := sourcesnapshot.Decode(after.Bytes)
	if err != nil {
		return ""
	}
	var before sourcesnapshot.Manifest
	if beforeDigest == "clean" {
		before.Target = current.Base
		before.Blobs = current.Blobs
	} else {
		info, err := os.Stat(file)
		if err != nil || info.Size() > sourcesnapshot.MaxBytes {
			return ""
		}
		raw, err := os.ReadFile(file)
		if err != nil || sourcesnapshot.Digest(raw) != beforeDigest {
			return ""
		}
		before, err = sourcesnapshot.Decode(raw)
		if err != nil {
			return ""
		}
	}
	diff, err := Diff(ctx, changedTrees(before, current))
	if err != nil || len(diff) > 128<<10 {
		return ""
	}
	return diff
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
