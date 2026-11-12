package livewatch

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Prompt startup only records Git paths and file metadata. Reading every source
// blob across a large workspace here can exceed the host's hook timeout. Actual
// changed repositories still get full content snapshots in the watcher.
func workspaceFingerprint(ctx context.Context, repo string) (string, error) {
	paths, err := filesystemGit(ctx, repo, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return "", err
	}
	head, err := filesystemGit(ctx, repo, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	var data bytes.Buffer
	data.Write(head)
	for _, raw := range bytes.Split(paths, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		name := string(raw)
		info, err := os.Lstat(filepath.Join(repo, name))
		if os.IsNotExist(err) {
			fmt.Fprintf(&data, "%s\x00missing\x00", name)
			continue
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&data, "%s\x00%d\x00%d\x00%d\x00", name, info.Mode(), info.Size(), info.ModTime().UnixNano())
	}
	return Key(data.String()), nil
}

// DiscoverRepositories follows directory nesting without a depth limit. Git
// metadata and dependency/cache trees are excluded; symlinks are not traversed.
func DiscoverRepositories(ctx context.Context, directory string) ([]string, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	if repo, err := Root(ctx, directory); err == nil {
		found[repo] = true
	}
	err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if path != directory {
			switch entry.Name() {
			case ".git", "node_modules", ".venv", "__pycache__", ".cache":
				return filepath.SkipDir
			}
		}
		if info, err := os.Lstat(filepath.Join(path, ".git")); err == nil && info.Mode()&os.ModeSymlink == 0 {
			if repo, err := Root(ctx, path); err == nil && repo == path {
				found[repo] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	repos := make([]string, 0, len(found))
	for repo := range found {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	return repos, nil
}

func filesystemGit(ctx context.Context, path string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "git", append([]string{"-C", path, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)...)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			c.Env = append(c.Env, v)
		}
	}
	c.Env = append(c.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	return c
}
func Root(ctx context.Context, path string) (string, error) {
	out, err := filesystemGit(ctx, path, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not a Git worktree")
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(out)))
}
