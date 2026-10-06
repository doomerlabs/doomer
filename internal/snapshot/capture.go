package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var ErrNoChanges = errors.New("no changes to review; select --base for committed branch changes")

type Options struct {
	Path, Base string
	Staged     bool
}

func git(ctx context.Context, root string, args ...string) ([]byte, error) {
	args = append([]string{"-C", root}, args...)
	c := exec.CommandContext(ctx, "git", args...)
	c.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null", "GIT_CONFIG_KEY_1=core.fsmonitor", "GIT_CONFIG_VALUE_1=false")
	b, err := c.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s failed", args[2])
	}
	return b, nil
}
func Capture(ctx context.Context, o Options) ([]byte, error) {
	if o.Path == "" {
		o.Path = "."
	}
	r, err := git(ctx, o.Path, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	root := strings.TrimSpace(string(r))
	if b, _ := git(ctx, root, "config", "--bool", "core.sparseCheckout"); strings.TrimSpace(string(b)) == "true" {
		return nil, fmt.Errorf("sparse checkouts are not supported")
	}
	// Two complete captures detect changed source, inventory, HEAD, and staged content.
	for attempt := 0; attempt < 3; attempt++ {
		a, err := capture(ctx, root, o)
		if errors.Is(err, ErrNoChanges) && o.Base == "" && !o.Staged {
			current, _ := git(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
			candidates := []string{}
			remote, _ := git(ctx, root, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
			if ref := strings.TrimSpace(string(remote)); ref != "" {
				candidates = append(candidates, ref)
			}
			candidates = append(candidates, "origin/main", "origin/master", "main", "master")
			for _, ref := range candidates {
				if _, e := git(ctx, root, "rev-parse", "--verify", ref+"^{commit}"); e != nil {
					continue
				}
				name := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/remotes/"), "origin/")
				if strings.TrimSpace(string(current)) == name {
					break
				}
				o.Base = ref
				a, err = capture(ctx, root, o)
				break
			}
		}
		if err != nil {
			return nil, err
		}
		b, err := capture(ctx, root, o)
		if err != nil {
			return nil, err
		}
		if bytes.Equal(a, b) {
			return a, nil
		}
	}
	return nil, fmt.Errorf("source changed during capture; retry when edits have settled")
}
func capture(ctx context.Context, root string, o Options) ([]byte, error) {
	h, err := git(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("a repository with a HEAD commit is required")
	}
	head := strings.TrimSpace(string(h))
	base := head
	scope := "worktree"
	if o.Staged {
		scope = "staged"
	}
	if o.Base != "" {
		resolved, e := git(ctx, root, "rev-parse", "--verify", "--end-of-options", o.Base+"^{commit}")
		if e != nil {
			return nil, e
		}
		b, e := git(ctx, root, "merge-base", strings.TrimSpace(string(resolved)), head)
		if e != nil {
			return nil, e
		}
		base = strings.TrimSpace(string(b))
		scope = "branch"
	}
	m := Manifest{Version: 1, Scope: scope, BaseRef: base, HeadRef: head, Base: []Entry{}, Target: []Entry{}, Blobs: map[string][]byte{}}
	m.Base, err = readTree(ctx, root, base, &m)
	if err != nil {
		return nil, err
	}
	idx, err := git(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	staged := []Entry{}
	for _, record := range bytes.Split(idx, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid index entry")
		}
		meta := strings.Fields(string(parts[0]))
		if len(meta) != 3 || meta[2] != "0" {
			return nil, fmt.Errorf("resolve merge conflicts before review")
		}
		p := string(parts[1])
		if meta[0] == "160000" {
			return nil, fmt.Errorf("submodules are not supported: %s", p)
		}
		paths[p] = meta[0]
		if o.Staged {
			staged = append(staged, Entry{p, meta[1], meta[0]})
		}
	}
	if o.Staged {
		ids := []string{}
		for _, e := range staged {
			ids = append(ids, e.Hash)
		}
		blobs, e := readObjects(ctx, root, ids)
		if e != nil {
			return nil, e
		}
		for _, entry := range staged {
			if e = add(&m, &m.Target, entry.Path, entry.Mode, blobs[entry.Hash]); e != nil {
				return nil, e
			}
		}
	}

	if !o.Staged {
		rootDir, e := os.OpenRoot(root)
		if e != nil {
			return nil, e
		}
		defer rootDir.Close()
		u, e := git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
		if e != nil {
			return nil, e
		}
		for _, p := range bytes.Split(u, []byte{0}) {
			if len(p) > 0 {
				paths[string(p)] = "100644"
			}
		}
		for p := range paths {
			if !ValidPath(p) {
				return nil, fmt.Errorf("unsupported path: %s", p)
			}
			full := filepath.Join(root, filepath.FromSlash(p))
			info, e := os.Lstat(full)
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return nil, e
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("only regular source files are supported: %s", p)
			}
			// Reject any parent link rather than reading outside the repository.
			for parent := filepath.Dir(full); parent != root; parent = filepath.Dir(parent) {
				i, e := os.Lstat(parent)
				if e != nil || i.Mode()&os.ModeSymlink != 0 {
					return nil, fmt.Errorf("linked parent directory: %s", p)
				}
			}
			if info.Size() > MaxFileBytes {
				return nil, fmt.Errorf("file exceeds 2 MiB: %s", p)
			}
			f, e := rootDir.Open(filepath.FromSlash(p))
			if e != nil {
				return nil, e
			}
			opened, e := f.Stat()
			if e != nil || !os.SameFile(info, opened) {
				f.Close()
				return nil, fmt.Errorf("source changed during capture: %s", p)
			}
			b, e := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
			closeErr := f.Close()
			if e != nil {
				return nil, e
			}
			if closeErr != nil {
				return nil, closeErr
			}
			mode := "100644"
			if info.Mode()&0111 != 0 {
				mode = "100755"
			}
			if e = add(&m, &m.Target, p, mode, b); e != nil {
				return nil, e
			}
		}
	}
	sort.Slice(m.Target, func(i, j int) bool { return m.Target[i].Path < m.Target[j].Path })
	// Do not submit an empty diff, even if Git's staged/unstaged status is dirty.
	same := len(m.Base) == len(m.Target)
	if same {
		for i := range m.Base {
			if m.Base[i] != m.Target[i] {
				same = false
				break
			}
		}
	}
	if same {
		return nil, ErrNoChanges
	}
	if err = m.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(m)
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("snapshot exceeds 16 MiB")
	}
	return data, err
}
func readTree(ctx context.Context, root, ref string, m *Manifest) ([]Entry, error) {
	out, err := git(ctx, root, "ls-tree", "-r", "-l", "-z", ref)
	if err != nil {
		return nil, err
	}
	type item struct {
		path, mode, oid string
		size            int
	}
	items := []item{}
	unique := map[string]bool{}
	total := 0
	for _, record := range bytes.Split(out, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid tree entry")
		}
		meta := strings.Fields(string(parts[0]))
		if len(meta) != 4 || meta[1] != "blob" {
			return nil, fmt.Errorf("submodules are not supported")
		}
		n, e := strconv.Atoi(meta[3])
		if e != nil || n > MaxFileBytes {
			return nil, fmt.Errorf("file exceeds 2 MiB: %s", parts[1])
		}
		if !ValidPath(string(parts[1])) || (meta[0] != "100644" && meta[0] != "100755") {
			return nil, fmt.Errorf("unsupported source path or file type: %s", parts[1])
		}
		items = append(items, item{string(parts[1]), meta[0], meta[2], n})
		if len(items) > MaxFiles {
			return nil, fmt.Errorf("snapshot exceeds file count limit")
		}
		if !unique[meta[2]] {
			unique[meta[2]] = true
			total += n

		}
	}
	if total > MaxBytes {
		return nil, fmt.Errorf("snapshot exceeds 16 MiB")
	}
	ids := []string{}
	for id := range unique {
		ids = append(ids, id)
	}
	blobs, err := readObjects(ctx, root, ids)
	if err != nil {
		return nil, err
	}
	entries := []Entry{}
	for _, item := range items {
		if err = add(m, &entries, item.path, item.mode, blobs[item.oid]); err != nil {
			return nil, err
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}
func readObjects(ctx context.Context, root string, ids []string) (map[string][]byte, error) {
	unique := map[string]bool{}
	var input strings.Builder
	for _, id := range ids {
		if !unique[id] {
			unique[id] = true
			input.WriteString(id + "\n")
		}
	}
	if len(unique) > MaxFiles {
		return nil, fmt.Errorf("snapshot exceeds file count limit")
	}
	batch := func(flag string) ([]byte, error) {
		c := exec.CommandContext(ctx, "git", "-C", root, "cat-file", flag)
		c.Stdin = strings.NewReader(input.String())
		b, e := c.Output()
		if e != nil {
			return nil, fmt.Errorf("read committed source failed")
		}
		return b, nil
	}
	checked, err := batch("--batch-check")
	if err != nil {
		return nil, err
	}
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(string(checked)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 || f[1] != "blob" {
			return nil, fmt.Errorf("source blob unavailable")
		}
		n, e := strconv.Atoi(f[2])
		if e != nil || n < 0 || n > MaxFileBytes {
			return nil, fmt.Errorf("source blob exceeds 2 MiB")
		}
		total += n
	}
	if total > MaxBytes {
		return nil, fmt.Errorf("snapshot exceeds 16 MiB")
	}
	content, err := batch("--batch")
	if err != nil {
		return nil, err
	}
	blobs := map[string][]byte{}
	for len(content) > 0 {
		i := bytes.IndexByte(content, '\n')
		if i < 0 {
			return nil, fmt.Errorf("invalid blob response")
		}
		f := strings.Fields(string(content[:i]))
		if len(f) != 3 || f[1] != "blob" {
			return nil, fmt.Errorf("invalid blob response")
		}
		n, e := strconv.Atoi(f[2])
		content = content[i+1:]
		if e != nil || n < 0 || n >= len(content) {
			return nil, fmt.Errorf("invalid blob response")
		}
		blobs[f[0]] = content[:n]
		content = content[n+1:]
	}
	return blobs, nil
}

func add(m *Manifest, entries *[]Entry, p, mode string, b []byte) error {
	if !ValidPath(p) || (mode != "100644" && mode != "100755") {
		return fmt.Errorf("unsupported source path or file type: %s", p)
	}
	if len(b) > MaxFileBytes {
		return fmt.Errorf("file exceeds 2 MiB: %s", p)
	}
	if len(*entries) >= MaxFiles {
		return fmt.Errorf("snapshot exceeds file count limit")
	}
	hash := Digest(b)
	m.Blobs[hash] = b
	*entries = append(*entries, Entry{p, hash, mode})
	total := 0
	for _, blob := range m.Blobs {
		total += len(blob)
	}
	if total > MaxBytes {
		return fmt.Errorf("snapshot exceeds 16 MiB")
	}
	return nil
}
