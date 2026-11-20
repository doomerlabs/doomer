package livewatch

import (
	"bytes"
	"context"
	"errors"
	"sort"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

type initialFile struct {
	body     []byte
	mode     string
	deleted  bool
	excluded string
}

// Only dirty files need preservation: the captured Git base supplies the rest.
func initialEditFiles(ctx context.Context, repo string) (map[string]initialFile, error) {
	_, head, err := sourcesnapshot.WorkspaceState(ctx, repo)
	if err != nil {
		return nil, err
	}
	if head == "" {
		// Use the capture reader so a full-directory baseline obeys the same
		// per-file and total content limits as the subsequent review snapshot.
		captured, err := sourcesnapshot.CaptureWorktree(ctx, sourcesnapshot.CaptureOptions{Path: repo, Workspace: true})
		if errors.Is(err, sourcesnapshot.ErrNoChanges) {
			return initialExclusions(captured), nil
		}
		if err != nil {
			return nil, err
		}
		manifest, err := sourcesnapshot.DecodeWorkspace(captured.Bytes)
		if err != nil {
			return nil, err
		}
		result := initialExclusions(captured)
		for _, entry := range manifest.Target {
			result[entry.Path] = initialFile{body: manifest.Blobs[entry.Hash], mode: entry.Mode}
		}
		return result, nil
	}
	base := "HEAD"
	for _, ref := range []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"} {
		if _, err := filesystemGit(ctx, repo, "rev-parse", "--verify", ref+"^{commit}").Output(); err == nil {
			raw, e := filesystemGit(ctx, repo, "merge-base", "HEAD", ref).Output()
			if e != nil {
				return nil, e
			}
			base = string(bytes.TrimSpace(raw))
			break
		}
	}
	changed, err := filesystemGit(ctx, repo, "diff", base, "--name-only", "-z").Output()
	if err != nil {
		return nil, err
	}
	untracked, err := filesystemGit(ctx, repo, "ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, name := range bytes.Split(append(changed, untracked...), []byte{0}) {
		if len(name) > 0 && !bytes.HasSuffix(name, []byte("/")) {
			paths = append(paths, string(name))
		}
	}
	result := map[string]initialFile{}
	err = sourcesnapshot.ReadReviewFiles(ctx, repo, paths, func(path string, data []byte, mode, reason string) {
		result[path] = initialFile{body: data, mode: mode, deleted: mode == "" && reason == "", excluded: reason}
	})
	return result, err
}

func initialExclusions(capture sourcesnapshot.Capture) map[string]initialFile {
	result := make(map[string]initialFile, len(capture.Skipped))
	for path, reason := range capture.Skipped {
		result[path] = initialFile{excluded: reason}
	}
	return result
}

func editScope(ctx context.Context, m sourcesnapshot.Manifest, baseline map[string]initialFile, excluded map[string]string) (string, error) {
	before := map[string]sourcesnapshot.Entry{}
	for _, e := range m.Base {
		before[e.Path] = e
	}
	blobs := map[string][]byte{}
	for h, b := range m.Blobs {
		blobs[h] = b
	}
	for path, f := range baseline {
		if f.excluded != "" || excluded[path] != "" {
			continue
		}
		if f.deleted {
			delete(before, path)
			continue
		}
		hash := sourcesnapshot.Digest(f.body)
		before[path] = sourcesnapshot.Entry{Path: path, Hash: hash, Mode: f.mode}
		blobs[hash] = f.body
	}
	m.Base = nil
	for _, e := range before {
		m.Base = append(m.Base, e)
	}
	sort.Slice(m.Base, func(i, j int) bool { return m.Base[i].Path < m.Base[j].Path })
	m.Blobs = blobs
	return Diff(ctx, m)
}
