package sourcesnapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// CaptureBaseline reads committed objects only, so an agent can keep editing
// while its already-consented repository baseline is prepared for upload.
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
	m.Base, err = g.readTree(base, m.Blobs)
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
	repo := LocalRepositoryIdentifier(top)
	if remote, e := g.text("config", "--get", "remote.origin.url"); e == nil {
		if id := repositoryIdentity(remote); id != "" {
			repo = id
		}
	}
	return m, repo, nil
}
