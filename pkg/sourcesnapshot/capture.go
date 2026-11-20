package sourcesnapshot

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
	// Workspace enables larger local snapshots for reference-based uploads.
	Workspace bool
	Path      string
	Base      string
	// Initial supplies the pre-edit tree for workspaces without a commit.
	Initial *Manifest
	// InitialExcluded records ineligible paths in the live edit baseline.
	InitialExcluded map[string]string
	ShowSkipped     bool
}

type SourceMetadata struct {
	Repository string `json:"repository,omitempty"`
	Branch     string `json:"branch,omitempty"`
	HeadSHA    string `json:"headSha"`
	Dirty      bool   `json:"dirty"`
}

// GitIdentity is the unverified identity configured for this checkout.
// It is review metadata, never part of the content-addressed source snapshot.
type GitIdentity struct {
	Name   string `json:"name,omitempty"`
	Email  string `json:"email,omitempty"`
	Source string `json:"source"`
}

type Capture struct {
	Skipped        map[string]string `json:",omitempty"`
	GitIdentity    *GitIdentity
	Bytes          []byte
	Digest         string
	BaseTreeHash   string
	TargetTreeHash string
	Source         SourceMetadata
}

type gitReader struct {
	workspace bool
	ctx       context.Context
	dir       string
	gitDir    string
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
	return g.runGit(input, false, args...)
}

func (g gitReader) runGit(input []byte, allowNoMatches bool, args ...string) ([]byte, error) {
	all := append([]string{"-C", g.dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
	if g.gitDir != "" {
		all = append([]string{"--git-dir=" + g.gitDir, "--work-tree=" + g.dir}, all...)
	}
	cmd := exec.CommandContext(g.ctx, "git", all...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1")
	cmd.Stdin = bytes.NewReader(input)
	output := &boundedBuffer{limit: limitsForWorkspace(g.workspace).content + MaxFiles*128}
	cmd.Stdout = output
	// Do not include Git stderr: it can contain credential-bearing remote URLs.
	if err := cmd.Run(); err != nil {
		if g.ctx.Err() != nil {
			return nil, g.ctx.Err()
		}
		var exit *exec.ExitError
		if allowNoMatches && errors.As(err, &exit) && exit.ExitCode() == 1 && !output.overflow {
			return output.Bytes(), nil
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
	if head == "" {
		return "", nil
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
// regular tracked files and non-ignored untracked files, skipping symlinks and
// paths beneath symlinks. It never stages files, runs
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
	g, cleanup, err := workspaceReader(ctx, directory, opts.Workspace)
	if err != nil {
		return result, err
	}
	defer cleanup()
	limits := limitsForWorkspace(opts.Workspace)
	top := g.dir
	if sparse, _ := g.text("config", "--bool", "core.sparseCheckout"); sparse == "true" {
		return result, fmt.Errorf("sparse worktrees are unsupported; review a full checkout")
	}
	head, err := g.workspaceHead()
	if err != nil {
		return result, err
	}
	base, err := g.baseline(strings.TrimSpace(opts.Base), head)
	if err != nil {
		return result, err
	}
	var headEntries []gitTreeEntry
	if head != "" {
		headEntries, err = g.treeEntries(head)
	}
	if err != nil {
		return result, err
	}
	headTree := map[string]gitTreeEntry{}
	for _, entry := range headEntries {
		headTree[entry.path] = entry
	}
	maxFile, err := reviewFileLimit(opts.Workspace)
	if err != nil {
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
	var baseline []gitTreeEntry
	switch {
	case base == "":
	case base == head:
		baseline = headEntries
	default:
		baseline, err = g.treeEntries(base)
		if err != nil {
			return result, err
		}
	}
	m := Manifest{Version: 1, Scope: "worktree", BaseRef: base, HeadRef: head, Blobs: map[string][]byte{}, Base: []Entry{}}
	var excluded map[string]string
	var policy []byte
	if opts.Workspace {
		policy, err = readIgnorePolicy(root)
		if err != nil {
			return result, err
		}
		paths := append(treePaths(baseline), names...)
		if opts.Initial != nil {
			for _, entry := range opts.Initial.Target {
				paths = append(paths, entry.Path)
			}
		}
		excluded, err = g.ignoredPaths(policy, paths)
		if err != nil {
			return result, err
		}
		if opts.Initial != nil {
			initialNames := make([]string, 0, len(opts.Initial.Target))
			for _, entry := range opts.Initial.Target {
				if excluded[entry.Path] == "" {
					initialNames = append(initialNames, entry.Path)
				}
			}
			if err := g.excludeGitIgnoredInitial(root, initialNames, excluded); err != nil {
				return result, err
			}
		}
		for name, reason := range opts.InitialExcluded {
			if reason != SkipDoomerIgnore && excluded[name] == "" {
				excluded[name] = reason
			}
		}
		result.Skipped = excluded
	}
	if base != "" {
		m.Base, err = g.readTreeEntries(baseline, m.Blobs, excluded, maxFile)
		if err != nil {
			return result, err
		}
	} else if opts.Initial != nil {
		for _, e := range opts.Initial.Target {
			if excluded[e.Path] == "" {
				m.Base = append(m.Base, e)
				m.Blobs[e.Hash] = opts.Initial.Blobs[e.Hash]
			}
		}
	}
	m.Target = []Entry{}
	total := 0
	for _, data := range m.Blobs {
		total += len(data)
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// Git can check out symlinks as ordinary files on Windows.
		if runtime.GOOS == "windows" && before.symlinks[name] {
			continue
		}
		if excluded[name] != "" {
			continue
		}
		data, mode, err := readWorktreeFile(root, name, maxFile)
		if opts.Workspace && errors.Is(err, errSourceTooLarge) {
			excluded[name] = SkipSize
			continue
		}
		if errors.Is(err, errSourceSymlink) {
			continue
		}
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
		if opts.Workspace && binarySource(data) {
			excluded[name] = SkipBinary
			continue
		}
		hash := Digest(data)
		if _, exists := m.Blobs[hash]; !exists {
			total += len(data)
			m.Blobs[hash] = data
		}
		if total > limits.content {
			return result, fmt.Errorf("snapshot content exceeds %d MiB", limits.content>>20)
		}
		m.Target = append(m.Target, Entry{Path: name, Hash: hash, Mode: mode})
		if expected, exists := headTree[name]; !exists || expected.mode != mode || expected.oid != gitBlobHash(data, len(head)) {
			dirty = true
		}
	}
	if opts.Workspace {
		pruneExcluded(&m, excluded)
		if opts.ShowSkipped {
			gitIgnored, err := g.ignoredUntracked()
			if err != nil {
				return result, err
			}
			for _, name := range gitIgnored {
				result.Skipped[name] = SkipGitIgnore
			}
		}
	}
	for name := range excluded {
		delete(headTree, name)
	}
	if len(m.Target) != len(headTree) {
		dirty = true
	}
	after, err := g.worktreeFiles()
	if err != nil {
		return result, err
	}
	endHead, err := g.workspaceHead()
	if err != nil {
		return result, err
	}
	if head != endHead || !bytes.Equal(before.index, after.index) || !equalNames(names, after.names) {
		return result, fmt.Errorf("worktree changed while capturing; try again after edits finish")
	}
	if opts.Workspace {
		currentPolicy, err := readIgnorePolicy(root)
		if err != nil {
			return result, err
		}
		if !bytes.Equal(policy, currentPolicy) {
			return result, fmt.Errorf("%s changed while capturing; try again after edits finish", ignoreFileName)
		}
	}
	if err := m.validate(opts.Workspace); err != nil {
		return result, fmt.Errorf("cannot review snapshot: %w (choose --base to review committed changes)", err)
	}
	result.Bytes, err = json.Marshal(m)
	if err != nil {
		return result, err
	}
	if len(result.Bytes) > limits.encoded {
		return Capture{}, fmt.Errorf("encoded snapshot exceeds %d MiB", limits.encoded>>20)
	}
	result.Digest = Digest(result.Bytes)
	result.BaseTreeHash = TreeHash(m.Base)
	result.TargetTreeHash = TreeHash(m.Target)
	result.Source = SourceMetadata{Repository: LocalRepositoryIdentifier(top), HeadSHA: head, Dirty: dirty}
	if branch, err := g.text("symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		result.Source.Branch = branch
	}
	if remote, err := g.text("config", "--get", "remote.origin.url"); err == nil {
		if repository := repositoryIdentity(remote); repository != "" {
			result.Source.Repository = repository
		}
	}
	result.GitIdentity = g.gitIdentity()
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
	names    []string
	index    []byte
	entries  map[string]gitTreeEntry
	symlinks map[string]bool
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
	result := worktreeInventory{entries: map[string]gitTreeEntry{}, symlinks: map[string]bool{}}
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
		names[name] = true
		if fields[0] == "120000" {
			result.symlinks[name] = true
			continue
		}
		if fields[0] != "100644" && fields[0] != "100755" {
			return result, fmt.Errorf("unsupported tracked file type at %q (submodules are unsupported)", name)
		}
		result.entries[name] = gitTreeEntry{path: name, oid: fields[1], mode: fields[0]}
	}
	args := []string{"ls-files", "--others", "--exclude-standard", "-z"}
	if g.gitDir != "" {
		for _, directory := range []string{"node_modules", ".venv", "__pycache__", ".cache"} {
			args = append(args, "--exclude="+directory+"/")
		}
	}
	untracked, err := g.run(nil, args...)
	if err != nil {
		return result, err
	}
	for _, name := range bytes.Split(untracked, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		// Git represents an untracked nested repository as a directory entry.
		// Its own watcher captures it; it is not a source file in this tree.
		if strings.HasSuffix(string(name), "/") {
			continue
		}
		names[string(name)] = true
	}
	if !g.workspace && len(names) > MaxFiles {
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

var (
	errSourceSymlink    = errors.New("source symlink")
	errSourceNonRegular = errors.New("not a regular file")
	errSourceTooLarge   = errors.New("file exceeds size limit")
)

func worktreePathInfo(root *os.Root, path string) (os.FileInfo, error) {
	// Skip symlinks at every component, including links to repository files.
	var info os.FileInfo
	parent := ""
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		parent = filepath.Join(parent, component)
		var err error
		info, err = root.Lstat(parent)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errSourceSymlink
		}
	}
	return info, nil
}

func readWorktreeFile(root *os.Root, name string, maxFileBytes int) ([]byte, string, error) {
	path := filepath.FromSlash(name)
	before, err := worktreePathInfo(root, path)
	if err != nil {
		return nil, "", err
	}
	if !before.Mode().IsRegular() {
		return nil, "", errSourceNonRegular
	}
	if before.Size() > int64(maxFileBytes) {
		return nil, "", fmt.Errorf("%w of %d bytes", errSourceTooLarge, maxFileBytes)
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
	data, err := io.ReadAll(io.LimitReader(file, int64(maxFileBytes)+1))
	if err != nil {
		return nil, "", err
	}
	after, err := file.Stat()
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxFileBytes || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
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
		if !ok || len(fields) != 3 || !ValidPath(name) {
			return nil, fmt.Errorf("invalid Git tree entry %q", name)
		}
		if fields[0] == "120000" && fields[1] == "blob" {
			continue
		}
		if fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return nil, fmt.Errorf("unsupported Git tree entry %q (submodules are unsupported)", name)
		}
		objects = append(objects, gitTreeEntry{path: name, oid: fields[2], mode: fields[0]})
	}
	if !g.workspace && len(objects) > MaxFiles {
		return nil, fmt.Errorf("Git tree exceeds the %d file limit", MaxFiles)
	}
	return objects, nil
}

// Read unique Git blobs in bounded batches and keep only eligible content.
func (g gitReader) readTreeEntries(objects []gitTreeEntry, blobs map[string][]byte, excluded map[string]string, fileLimit int) ([]Entry, error) {
	eligible := make([]gitTreeEntry, 0, len(objects))
	unique := map[string]bool{}
	var ids []string
	for _, object := range objects {
		if excluded[object.path] != "" {
			continue
		}
		eligible = append(eligible, object)
		if !unique[object.oid] {
			unique[object.oid] = true
			ids = append(ids, object.oid)
		}
	}
	hashes := map[string]string{}
	reasons := map[string]string{}
	if len(ids) > 0 {
		input := []byte(strings.Join(ids, "\n") + "\n")
		raw, err := g.run(input, "cat-file", "--batch-check")
		if err != nil {
			return nil, err
		}
		sizes, err := parseTreeSizes(raw, ids)
		if err != nil {
			return nil, err
		}
		limits := limitsForWorkspace(g.workspace)
		var batch []string
		batchBytes, total := 0, 0
		if excluded == nil {
			for _, size := range sizes {
				if size > fileLimit {
					return nil, fmt.Errorf("baseline file exceeds %d bytes", fileLimit)
				}
				total += size
				if total > limits.content {
					return nil, fmt.Errorf("baseline content exceeds %d MiB", limits.content>>20)
				}
			}
			total = 0
		}
		for i, id := range ids {
			size := sizes[i]
			if size > fileLimit {
				reasons[id] = SkipSize
				continue
			}
			if batchBytes+size > min(limits.content, 32<<20) {
				if err := g.readBlobBatch(batch, fileLimit, blobs, hashes, reasons, excluded != nil, &total); err != nil {
					return nil, err
				}
				batch, batchBytes = batch[:0], 0
			}
			batch, batchBytes = append(batch, id), batchBytes+size
		}
		if err := g.readBlobBatch(batch, fileLimit, blobs, hashes, reasons, excluded != nil, &total); err != nil {
			return nil, err
		}
	}
	entries := make([]Entry, 0, len(eligible))
	for _, object := range eligible {
		if reason := reasons[object.oid]; reason != "" {
			excluded[object.path] = reason
			continue
		}
		entries = append(entries, Entry{Path: object.path, Hash: hashes[object.oid], Mode: object.mode})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// RepositoryIdentifier strips credentials and rejects local filesystem remotes.
func RepositoryIdentifier(remote string) string { return repositoryIdentity(remote) }

// LocalRepositoryIdentifier includes a display name and an opaque checkout key.
// Only the directory name is transmitted, while same-named checkouts stay distinct.
func LocalRepositoryIdentifier(directory string) string {
	if absolute, err := filepath.Abs(directory); err == nil {
		directory = absolute
	}
	if resolved, err := filepath.EvalSymlinks(directory); err == nil {
		directory = resolved
	}
	directory = filepath.Clean(directory)
	name := filepath.Base(directory)
	return "local/" + url.PathEscape(name) + "/" + Digest([]byte(directory))
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

func parseTreeSizes(data []byte, ids []string) ([]int, error) {
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != len(ids) {
		return nil, fmt.Errorf("incomplete baseline object sizes")
	}
	sizes := make([]int, 0, len(ids))
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != ids[i] || fields[1] != "blob" {
			return nil, fmt.Errorf("invalid baseline object size")
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 {
			return nil, fmt.Errorf("invalid baseline object size")
		}
		sizes = append(sizes, size)
	}
	return sizes, nil
}

func (g gitReader) readBlobBatch(ids []string, fileLimit int, blobs map[string][]byte, hashes, reasons map[string]string, filterBinary bool, total *int) error {
	if len(ids) == 0 {
		return nil
	}
	data, err := g.run([]byte(strings.Join(ids, "\n")+"\n"), "cat-file", "--batch")
	if err != nil {
		return err
	}
	reader := bufio.NewReader(bytes.NewReader(data))
	for _, id := range ids {
		header, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("incomplete baseline object")
		}
		fields := strings.Fields(header)
		if len(fields) != 3 || fields[0] != id || fields[1] != "blob" {
			return fmt.Errorf("invalid baseline object")
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 || size > fileLimit {
			return fmt.Errorf("invalid baseline object size")
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(reader, content); err != nil {
			return err
		}
		if newline, err := reader.ReadByte(); err != nil || newline != '\n' {
			return fmt.Errorf("invalid baseline object framing")
		}
		if filterBinary && binarySource(content) {
			reasons[id] = SkipBinary
			continue
		}
		hash := Digest(content)
		hashes[id] = hash
		if _, exists := blobs[hash]; !exists {
			blobs[hash] = content
			*total += size
		}
		limit := limitsForWorkspace(g.workspace).content
		if *total > limit {
			return fmt.Errorf("baseline content exceeds %d MiB", limit>>20)
		}
	}
	return nil
}
