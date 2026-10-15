package sourcesnapshot

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type CaptureOptions struct {
	Path string
	Base string
}

type SourceMetadata struct {
	Repository string `json:"repository,omitempty"`
	Branch     string `json:"branch,omitempty"`
	HeadSHA    string `json:"headSha"`
	Dirty      bool   `json:"dirty"`
}

type Capture struct {
	Bytes          []byte
	Digest         string
	BaseTreeHash   string
	TargetTreeHash string
	Source         SourceMetadata
}

type gitReader struct {
	ctx context.Context
	dir string
}

type boundedBuffer struct {
	data     bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.data.Len() {
		b.overflow = true
		return 0, fmt.Errorf("snapshot exceeds upload limits")
	}
	return b.data.Write(p)
}

func (b *boundedBuffer) Bytes() []byte { return b.data.Bytes() }

// run only uses Git's local read commands. Disable optional index writes and
// ignore inherited repository overrides so --path always selects the source.
func (g gitReader) run(input []byte, args ...string) ([]byte, error) {
	all := append([]string{"-C", g.dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.CommandContext(g.ctx, "git", all...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1")
	cmd.Stdin = bytes.NewReader(input)
	output := &boundedBuffer{limit: MaxBytes + MaxFiles*128}
	cmd.Stdout = output
	// Do not include Git stderr: it can contain credential-bearing remote URLs.
	if err := cmd.Run(); err != nil {
		if g.ctx.Err() != nil {
			return nil, g.ctx.Err()
		}
		if output.overflow {
			return nil, fmt.Errorf("snapshot exceeds upload limits")
		}
		return nil, fmt.Errorf("git %s failed (requires a local Git worktree with a commit)", args[0])
	}
	return output.Bytes(), nil
}
func (g gitReader) text(args ...string) (string, error) {
	data, err := g.run(nil, args...)
	return strings.TrimSpace(string(data)), err
}
func (g gitReader) commit(ref string) (string, error) {
	return g.text("rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
}
func (g gitReader) baseline(explicit, head string) (string, error) {
	if explicit != "" {
		base, err := g.commit(explicit)
		if err != nil {
			return "", fmt.Errorf("cannot resolve --base %q to a local commit", explicit)
		}
		return base, nil
	}
	candidates := []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"}
	for _, candidate := range candidates {
		base, err := g.commit(candidate)
		if err != nil {
			continue
		}
		mergeBase, err := g.text("merge-base", head, base)
		if err != nil {
			return "", fmt.Errorf("cannot find a merge base with %s; supply --base", candidate)
		}
		return mergeBase, nil
	}
	// Repositories without a default-branch ref can still review uncommitted work.
	return head, nil
}

// CaptureWorktree captures the whole repository containing Path, including
// tracked files and non-ignored untracked files. It never stages files, runs
// filters/builds/hooks, fetches, or writes Git objects or repository metadata.
func CaptureWorktree(ctx context.Context, opts CaptureOptions) (Capture, error) {
	var result Capture
	directory := opts.Path
	if directory == "" {
		directory = "."
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return result, err
	}
	g := gitReader{ctx: ctx, dir: directory}
	topBytes, err := g.run(nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return result, err
	}
	top := strings.TrimSuffix(string(topBytes), "\n")
	if runtime.GOOS == "windows" {
		top = strings.TrimSuffix(top, "\r")
	}
	g.dir = top
	if sparse, _ := g.text("config", "--bool", "core.sparseCheckout"); sparse == "true" {
		return result, fmt.Errorf("sparse worktrees are unsupported; review a full checkout")
	}
	head, err := g.commit("HEAD")
	if err != nil {
		return result, err
	}
	base, err := g.baseline(strings.TrimSpace(opts.Base), head)
	if err != nil {
		return result, err
	}
	headEntries, err := g.treeEntries(head)
	if err != nil {
		return result, err
	}
	headTree := map[string]gitTreeEntry{}
	for _, entry := range headEntries {
		headTree[entry.path] = entry
	}
	m := Manifest{Version: 1, Scope: "worktree", BaseRef: base, HeadRef: head, Blobs: map[string][]byte{}}
	if m.Base, err = g.readTree(base, m.Blobs); err != nil {
		return result, err
	}
	before, err := g.worktreeFiles()
	if err != nil {
		return result, err
	}
	names := before.names
	dirty := !sameGitTree(headTree, before.entries)
	root, err := os.OpenRoot(top)
	if err != nil {
		return result, err
	}
	defer root.Close()
	m.Target = []Entry{}
	total := 0
	for _, data := range m.Blobs {
		total += len(data)
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		data, mode, err := readWorktreeFile(root, name)
		if os.IsNotExist(err) {
			// A tracked deletion belongs only in base.
			continue
		}
		if err != nil {
			return result, fmt.Errorf("capture %q: %w", name, err)
		}
		if runtime.GOOS == "windows" {
			// Windows does not expose Unix executable bits; preserve the index mode.
			mode = "100644"
			if entry, exists := before.entries[name]; exists {
				mode = entry.mode
			}
		}
		hash := Digest(data)
		if _, exists := m.Blobs[hash]; !exists {
			total += len(data)
			m.Blobs[hash] = data
		}
		if total > MaxBytes {
			return result, fmt.Errorf("snapshot content exceeds 16 MiB")
		}
		m.Target = append(m.Target, Entry{Path: name, Hash: hash, Mode: mode})
		if expected, exists := headTree[name]; !exists || expected.mode != mode || expected.oid != gitBlobHash(data, len(head)) {
			dirty = true
		}
	}
	if len(m.Target) != len(headTree) {
		dirty = true
	}
	after, err := g.worktreeFiles()
	if err != nil {
		return result, err
	}
	endHead, err := g.commit("HEAD")
	if err != nil {
		return result, err
	}
	if head != endHead || !bytes.Equal(before.index, after.index) || !equalNames(names, after.names) {
		return result, fmt.Errorf("worktree changed while capturing; try again after edits finish")
	}
	if err := m.Validate(); err != nil {
		return result, fmt.Errorf("cannot review snapshot: %w (choose --base to review committed changes)", err)
	}
	result.Bytes, err = json.Marshal(m)
	if err != nil {
		return result, err
	}
	if len(result.Bytes) > MaxBytes {
		return Capture{}, fmt.Errorf("encoded snapshot exceeds the 16 MiB upload limit")
	}
	result.Digest = Digest(result.Bytes)
	result.BaseTreeHash = TreeHash(m.Base)
	result.TargetTreeHash = TreeHash(m.Target)
	result.Source = SourceMetadata{HeadSHA: head, Dirty: dirty}
	if branch, err := g.text("symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		result.Source.Branch = branch
	}
	if remote, err := g.text("config", "--get", "remote.origin.url"); err == nil {
		result.Source.Repository = repositoryIdentity(remote)
	}
	return result, nil
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type gitTreeEntry struct{ path, oid, mode string }
type worktreeInventory struct {
	names   []string
	index   []byte
	entries map[string]gitTreeEntry
}

func sameGitTree(a, b map[string]gitTreeEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for name, entry := range a {
		if entry != b[name] {
			return false
		}
	}
	return true
}

// Git blob IDs compare raw captured bytes to HEAD without invoking configured
// clean filters or fsmonitor hooks through git status/diff. They are metadata
// only; the portable content fingerprint always uses SHA-256.
func gitBlobHash(data []byte, objectIDLength int) string {
	var h hash.Hash = sha1.New()
	if objectIDLength == 64 {
		h = sha256.New()
	}
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (g gitReader) worktreeFiles() (worktreeInventory, error) {
	result := worktreeInventory{entries: map[string]gitTreeEntry{}}
	index, err := g.run(nil, "ls-files", "--stage", "-z")
	if err != nil {
		return result, err
	}
	result.index = index
	names := map[string]bool{}
	for _, line := range bytes.Split(index, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		header, name, ok := strings.Cut(string(line), "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || fields[2] != "0" {
			return result, fmt.Errorf("resolve Git merge conflicts before reviewing")
		}
		if fields[0] != "100644" && fields[0] != "100755" {
			return result, fmt.Errorf("unsupported tracked file type at %q (symlinks and submodules are unsupported)", name)
		}
		names[name] = true
		result.entries[name] = gitTreeEntry{path: name, oid: fields[1], mode: fields[0]}
	}
	untracked, err := g.run(nil, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return result, err
	}
	for _, name := range bytes.Split(untracked, []byte{0}) {
		if len(name) > 0 {
			names[string(name)] = true
		}
	}
	if len(names) > MaxFiles {
		return result, fmt.Errorf("snapshot exceeds the %d file limit", MaxFiles)
	}
	result.names = make([]string, 0, len(names))
	for name := range names {
		if !ValidPath(name) {
			return result, fmt.Errorf("unsupported source path %q", name)
		}
		result.names = append(result.names, name)
	}
	sort.Strings(result.names)
	return result, nil
}

func readWorktreeFile(root *os.Root, name string) ([]byte, string, error) {
	path := filepath.FromSlash(name)
	// Reject symlinks at every component, even ones that point inside the root.
	for parent := path; parent != "."; parent = filepath.Dir(parent) {
		info, err := root.Lstat(parent)
		if err != nil {
			return nil, "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, "", fmt.Errorf("symlinks are unsupported")
		}
	}
	before, err := root.Lstat(path)
	if err != nil {
		return nil, "", err
	}
	if !before.Mode().IsRegular() || before.Size() > MaxFileBytes {
		return nil, "", fmt.Errorf("requires a regular file no larger than 2 MiB")
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return nil, "", fmt.Errorf("file changed while capturing")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return nil, "", err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if len(data) > MaxFileBytes || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
		return nil, "", fmt.Errorf("file changed while capturing")
	}
	mode := "100644"
	if before.Mode().Perm()&0111 != 0 {
		mode = "100755"
	}
	return data, mode, nil
}

func (g gitReader) treeEntries(commit string) ([]gitTreeEntry, error) {
	tree, err := g.run(nil, "ls-tree", "-r", "-z", commit)
	if err != nil {
		return nil, err
	}
	var objects []gitTreeEntry
	for _, line := range bytes.Split(tree, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		header, name, ok := strings.Cut(string(line), "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") || !ValidPath(name) {
			return nil, fmt.Errorf("unsupported Git tree entry %q (symlinks and submodules are unsupported)", name)
		}
		objects = append(objects, gitTreeEntry{path: name, oid: fields[2], mode: fields[0]})
	}
	if len(objects) > MaxFiles {
		return nil, fmt.Errorf("Git tree exceeds the %d file limit", MaxFiles)
	}
	return objects, nil
}

// Read all unique Git blobs through one batch process, rather than spawning a
// Git process per file. Bound both the subprocess output and decoded content.
func (g gitReader) readTree(commit string, blobs map[string][]byte) ([]Entry, error) {
	objects, err := g.treeEntries(commit)
	if err != nil {
		return nil, err
	}
	unique := map[string]bool{}
	var ids []string
	for _, object := range objects {
		if !unique[object.oid] {
			unique[object.oid] = true
			ids = append(ids, object.oid)
		}
	}
	hashes := map[string]string{}
	if len(ids) > 0 {
		data, err := g.run([]byte(strings.Join(ids, "\n")+"\n"), "cat-file", "--batch")
		if err != nil {
			return nil, err
		}
		reader := bufio.NewReader(bytes.NewReader(data))
		total := 0
		for _, id := range ids {
			header, err := reader.ReadString('\n')
			if err != nil {
				return nil, fmt.Errorf("incomplete baseline object")
			}
			fields := strings.Fields(header)
			if len(fields) != 3 || fields[0] != id || fields[1] != "blob" {
				return nil, fmt.Errorf("invalid baseline object")
			}
			size, err := strconv.Atoi(fields[2])
			if err != nil || size < 0 || size > MaxFileBytes {
				return nil, fmt.Errorf("baseline file exceeds 2 MiB")
			}
			content := make([]byte, size)
			if _, err := io.ReadFull(reader, content); err != nil {
				return nil, err
			}
			if newline, err := reader.ReadByte(); err != nil || newline != '\n' {
				return nil, fmt.Errorf("invalid baseline object framing")
			}
			hash := Digest(content)
			hashes[id] = hash
			if _, exists := blobs[hash]; !exists {
				blobs[hash] = content
				total += size
			}
			if total > MaxBytes {
				return nil, fmt.Errorf("baseline content exceeds 16 MiB")
			}
		}
	}
	entries := make([]Entry, 0, len(objects))
	for _, object := range objects {
		entries = append(entries, Entry{Path: object.path, Hash: hashes[object.oid], Mode: object.mode})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func repositoryIdentity(remote string) string {
	if !strings.Contains(remote, "://") {
		if at := strings.IndexByte(remote, '@'); at >= 0 {
			if colon := strings.IndexByte(remote[at+1:], ':'); colon >= 0 {
				remote = "ssh://" + remote[:at+1+colon] + "/" + remote[at+2+colon:]
			}
		}
	}
	u, err := url.Parse(remote)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http" && u.Scheme != "ssh" && u.Scheme != "git") {
		return ""
	}
	// Local filesystem remotes, userinfo, query strings, and fragments are never stored.
	name := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	identity := strings.ToLower(u.Host) + "/" + name
	for _, c := range identity {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("._-/:~", c)) {
			return ""
		}
	}
	if name == "" || len(identity) > 512 {
		return ""
	}
	return identity
}
