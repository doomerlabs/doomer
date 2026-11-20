package sourcesnapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	DefaultReviewFileBytes = 1 << 20
	ReviewFileLimitEnv     = "DOOMER_REVIEW_MAX_FILE_BYTES"
	ignoreFileName         = ".doomerignore"
)

const (
	SkipDoomerIgnore = ".doomerignore"
	SkipGitIgnore    = "Git ignore rules"
	SkipBinary       = "binary"
	SkipSize         = "over the review file size limit"
)

func reviewFileLimit(workspace bool) (int, error) {
	if !workspace {
		return limitsForWorkspace(false).file, nil
	}
	raw := os.Getenv(ReviewFileLimitEnv)
	if raw == "" {
		return DefaultReviewFileBytes, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > MaxWorkspaceFileBytes {
		return 0, fmt.Errorf("%s must be an integer from 1 to %d bytes", ReviewFileLimitEnv, MaxWorkspaceFileBytes)
	}
	return limit, nil
}

func binarySource(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data)
}

func readIgnorePolicy(root *os.Root) ([]byte, error) {
	policy, _, err := readWorktreeFile(root, ignoreFileName, MaxFileBytes)
	if os.IsNotExist(err) || errors.Is(err, errSourceSymlink) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ignoreFileName, err)
	}
	return policy, nil
}

// Match independent of the source repository's index, .gitignore files and
// local excludes. Git evaluates directory pruning, anchoring and negation.
func (g gitReader) ignoredPaths(policy []byte, names []string) (map[string]string, error) {
	if len(policy) == 0 || len(names) == 0 {
		return map[string]string{}, nil
	}
	scratch, err := os.MkdirTemp("", "doomer-ignore-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	matcher := gitReader{ctx: g.ctx, dir: scratch, workspace: g.workspace}
	if _, err := matcher.run(nil, "init", "--template="); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(scratch, ".gitignore"), policy, 0600); err != nil {
		return nil, err
	}
	input := []byte(strings.Join(names, "\x00") + "\x00")
	output, err := matcher.runGit(input, true, "-c", "core.excludesFile=/dev/null", "check-ignore", "--no-index", "-z", "--stdin")
	if err != nil {
		return nil, err
	}
	ignored := map[string]string{}
	for path := range bytes.SplitSeq(output, []byte{0}) {
		if len(path) != 0 {
			ignored[string(path)] = SkipDoomerIgnore
		}
	}
	return ignored, nil
}

// A previously visible untracked file may become Git-ignored during a turn.
func (g gitReader) excludeGitIgnoredInitial(root *os.Root, names []string, excluded map[string]string) error {
	candidates := names[:0]
	for _, name := range names {
		// Git rejects the entire batch when a path crosses a symlink.
		if _, err := worktreePathInfo(root, filepath.FromSlash(name)); !errors.Is(err, errSourceSymlink) {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	input := []byte(strings.Join(candidates, "\x00") + "\x00")
	output, err := g.runGit(input, true, "check-ignore", "-z", "--stdin")
	if err != nil {
		return err
	}
	for path := range bytes.SplitSeq(output, []byte{0}) {
		if len(path) != 0 {
			excluded[string(path)] = SkipGitIgnore
		}
	}
	return nil
}

func (g gitReader) ignoredUntracked() ([]string, error) {
	output, err := g.run(nil, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil, err
	}
	var names []string
	for path := range bytes.SplitSeq(output, []byte{0}) {
		if len(path) != 0 {
			names = append(names, string(path))
		}
	}
	return names, nil
}

// ReadReviewFiles captures the live watcher's pre-edit versions of dirty files.
// Missing paths are delivered with an empty mode and excluded paths with a reason.
func ReadReviewFiles(ctx context.Context, directory string, names []string, visit func(path string, data []byte, mode, reason string)) error {
	g, cleanup, err := workspaceReader(ctx, directory, true)
	if err != nil {
		return err
	}
	defer cleanup()
	root, err := os.OpenRoot(g.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	limit, err := reviewFileLimit(true)
	if err != nil {
		return err
	}
	policy, err := readIgnorePolicy(root)
	if err != nil {
		return err
	}
	ignored, err := g.ignoredPaths(policy, names)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !ValidPath(name) {
			continue
		}
		if reason := ignored[name]; reason != "" {
			visit(name, nil, "", reason)
			continue
		}
		data, mode, err := readWorktreeFile(root, name, limit)
		switch {
		case errors.Is(err, errSourceSymlink), errors.Is(err, errSourceNonRegular):
			continue
		case errors.Is(err, errSourceTooLarge):
			visit(name, nil, "", SkipSize)
		case os.IsNotExist(err):
			visit(name, nil, "", "")
		case err != nil:
			return fmt.Errorf("capture %q: %w", name, err)
		case binarySource(data):
			visit(name, nil, "", SkipBinary)
		default:
			visit(name, data, mode, "")
		}
	}
	return nil
}

func treePaths(entries []gitTreeEntry) []string {
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		paths = append(paths, entry.path)
	}
	return paths
}

func pruneExcluded(m *Manifest, excluded map[string]string) {
	used := map[string]bool{}
	for _, tree := range []*[]Entry{&m.Base, &m.Target} {
		kept := (*tree)[:0]
		for _, e := range *tree {
			if excluded[e.Path] == "" {
				kept = append(kept, e)
				used[e.Hash] = true
			}
		}
		*tree = kept
	}
	for hash := range m.Blobs {
		if !used[hash] {
			delete(m.Blobs, hash)
		}
	}
}

func WriteSkippedPaths(w io.Writer, skipped map[string]string) error {
	paths := make([]string, 0, len(skipped))
	for path := range skipped {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if _, err := fmt.Fprintf(w, "Skipped %q (%s)\n", path, skipped[path]); err != nil {
			return err
		}
	}
	return nil
}
