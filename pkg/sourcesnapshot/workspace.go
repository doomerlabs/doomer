package sourcesnapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// workspaceReader uses disposable metadata to apply Git's ignore rules to plain
// directories. It never initializes or writes metadata in the source directory.
func workspaceReader(ctx context.Context, directory string, workspace bool) (gitReader, func(), error) {
	g := gitReader{ctx: ctx, dir: directory, workspace: workspace}
	noop := func() {}
	top, err := g.run(nil, "rev-parse", "--show-toplevel")
	if err == nil {
		g.dir = strings.TrimSuffix(strings.TrimSuffix(string(top), "\n"), "\r")
		return g, noop, nil
	}
	if !workspace {
		return g, noop, err
	}
	for p := directory; ; p = filepath.Dir(p) {
		if _, e := os.Lstat(filepath.Join(p, ".git")); !os.IsNotExist(e) {
			return g, noop, err
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	info, e := os.Stat(directory)
	if e != nil {
		return g, noop, e
	}
	if !info.IsDir() {
		return g, noop, fmt.Errorf("workspace is not a directory")
	}
	metadata, e := os.MkdirTemp("", "doomer-workspace-git-*")
	if e != nil {
		return g, noop, e
	}
	cleanup := func() { os.RemoveAll(metadata) }
	if _, e = g.run(nil, "init", "--bare", "--template=", metadata); e != nil {
		cleanup()
		return g, noop, e
	}
	g.gitDir = metadata
	return g, cleanup, nil
}

func (g gitReader) workspaceHead() (string, error) {
	head, err := g.commit("HEAD")
	if err == nil {
		return head, nil
	}
	// Only an unborn branch may use the empty baseline. Existing refs must still
	// fail capture when they are corrupt or unreadable.
	ref, e := g.text("symbolic-ref", "--quiet", "HEAD")
	if e != nil || !strings.HasPrefix(ref, "refs/heads/") {
		return "", err
	}
	args := []string{"-C", g.dir}
	if g.gitDir != "" {
		args = append(args, "--git-dir="+g.gitDir, "--work-tree="+g.dir)
	}
	args = append(args, "show-ref", "--verify", "--quiet", ref)
	cmd := exec.CommandContext(g.ctx, "git", args...)
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	var exit *exec.ExitError
	if errors.As(cmd.Run(), &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	return "", err
}

// WorkspaceState lists the same paths that capture reads. Head is empty for
// plain directories and unborn Git repositories.
func WorkspaceState(ctx context.Context, directory string) ([]string, string, error) {
	g, cleanup, err := workspaceReader(ctx, directory, true)
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	head, err := g.workspaceHead()
	if err != nil {
		return nil, "", err
	}
	files, err := g.worktreeFiles()
	return files.names, head, err
}
