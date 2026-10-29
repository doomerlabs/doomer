package watch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Root(ctx context.Context, path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", p, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s is not a Git worktree", path)
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(out)))
}
func Diff(ctx context.Context, m sourcesnapshot.Manifest) (string, error) {
	directory, err := os.MkdirTemp("", "doomer-watch-diff-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(directory)
	for name, entries := range map[string][]sourcesnapshot.Entry{"a": m.Base, "b": m.Target} {
		if err = os.MkdirAll(filepath.Join(directory, name), 0700); err != nil {
			return "", err
		}
		for _, e := range entries {
			if !sourcesnapshot.ValidPath(e.Path) {
				return "", fmt.Errorf("invalid diff path")
			}
			file := filepath.Join(directory, name, filepath.FromSlash(e.Path))
			if err = os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				return "", err
			}
			mode := os.FileMode(0600)
			if e.Mode == "100755" {
				mode = 0700
			}
			if err = os.WriteFile(file, m.Blobs[e.Hash], mode); err != nil {
				return "", err
			}
		}
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "diff", "--no-index", "--no-prefix", "--no-color", "--no-ext-diff", "--no-textconv", "--", "a", "b")
	cmd.Dir = directory
	var output limitedDiff
	cmd.Stdout = &output
	if err = cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return "", fmt.Errorf("could not compute review diff")
		}
	}
	if output.overflow {
		return "", fmt.Errorf("diff exceeds 16 MiB")
	}
	return output.String(), nil
}

type limitedDiff struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedDiff) Write(p []byte) (int, error) {
	if b.Len()+len(p) > sourcesnapshot.MaxBytes {
		b.overflow = true
		return 0, fmt.Errorf("diff limit")
	}
	return b.Buffer.Write(p)
}
