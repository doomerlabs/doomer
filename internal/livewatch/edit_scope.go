package livewatch

import (
	"bytes"
	"context"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"os"
	"path/filepath"
	"sort"
)

type initialFile struct {
	body    []byte
	mode    string
	deleted bool
}

// Only dirty files need preservation: the captured Git base supplies the rest.
func initialEditFiles(ctx context.Context, repo string) (map[string]initialFile, error) {
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
	result := map[string]initialFile{}
	for _, name := range bytes.Split(append(changed, untracked...), []byte{0}) {
		if len(name) == 0 || bytes.HasSuffix(name, []byte("/")) {
			continue
		}
		path := string(name)
		if !sourcesnapshot.ValidPath(path) {
			continue
		}
		info, err := os.Lstat(filepath.Join(repo, path))
		if os.IsNotExist(err) {
			result[path] = initialFile{deleted: true}
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(repo, path))
		if err != nil {
			return nil, err
		}
		mode := "100644"
		if info.Mode()&0111 != 0 {
			mode = "100755"
		}
		result[path] = initialFile{body: body, mode: mode}
	}
	return result, nil
}
func editScope(ctx context.Context, m sourcesnapshot.Manifest, baseline map[string]initialFile) (string, error) {
	before := map[string]sourcesnapshot.Entry{}
	for _, e := range m.Base {
		before[e.Path] = e
	}
	blobs := map[string][]byte{}
	for h, b := range m.Blobs {
		blobs[h] = b
	}
	for path, f := range baseline {
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
