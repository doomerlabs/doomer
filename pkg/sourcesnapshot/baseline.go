package sourcesnapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CaptureBaseline prepares committed source for upload. It checks eligibility
// against the worktree without including worktree contents in the manifest.
func CaptureBaseline(ctx context.Context, opts CaptureOptions) (Manifest, string, error) {
	path, err := filepath.Abs(opts.Path)
	if err != nil {
		return Manifest{}, "", err
	}
	g := gitReader{ctx: ctx, dir: path, workspace: true}
	top, err := g.text("rev-parse", "--show-toplevel")
	if err != nil {
		return Manifest{}, "", err
	}
	g.dir = top
	head, err := g.commit("HEAD")
	if err != nil {
		return Manifest{}, "", err
	}
	base, err := g.baseline(strings.TrimSpace(opts.Base), head)
	if err != nil {
		return Manifest{}, "", err
	}
	m := Manifest{Version: 1, Scope: "worktree", BaseRef: base, HeadRef: head, Blobs: map[string][]byte{}}
	root, err := os.OpenRoot(top)
	if err != nil {
		return Manifest{}, "", err
	}
	defer root.Close()
	maxFile, err := reviewFileLimit(true)
	if err != nil {
		return Manifest{}, "", err
	}
	entries, err := g.treeEntries(base)
	if err != nil {
		return Manifest{}, "", err
	}
	policy, err := readIgnorePolicy(root)
	if err != nil {
		return Manifest{}, "", err
	}
	excluded, err := g.ignoredPaths(policy, treePaths(entries))
	if err != nil {
		return Manifest{}, "", err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Manifest{}, "", err
		}
		if excluded[entry.path] != "" {
			continue
		}
		data, _, err := readWorktreeFile(root, entry.path, maxFile)
		switch {
		case errors.Is(err, errSourceTooLarge):
			excluded[entry.path] = SkipSize
		case os.IsNotExist(err), errors.Is(err, errSourceSymlink), errors.Is(err, errSourceNonRegular):
			// Deletions and paths replaced by a directory can have a reviewable base.
		case err != nil:
			return Manifest{}, "", fmt.Errorf("capture %q: %w", entry.path, err)
		case binarySource(data):
			excluded[entry.path] = SkipBinary
		}
	}
	m.Base, err = g.readTreeEntries(entries, m.Blobs, excluded, maxFile)
	if err != nil {
		return Manifest{}, "", err
	}
	total := 0
	for _, b := range m.Blobs {
		total += len(b)
	}
	if total > MaxWorkspaceBytes {
		return Manifest{}, "", fmt.Errorf("baseline exceeds workspace limit")
	}
	currentPolicy, err := readIgnorePolicy(root)
	if err != nil {
		return Manifest{}, "", err
	}
	if !bytes.Equal(policy, currentPolicy) {
		return Manifest{}, "", fmt.Errorf("%s changed while capturing; try again after edits finish", ignoreFileName)
	}
	repo := LocalRepositoryIdentifier(top)
	if remote, e := g.text("config", "--get", "remote.origin.url"); e == nil {
		if id := repositoryIdentity(remote); id != "" {
			repo = id
		}
	}
	return m, repo, nil
}
