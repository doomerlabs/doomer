package sourcesnapshot

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Snapshot Test", "GIT_AUTHOR_EMAIL=snapshot@example.test", "GIT_COMMITTER_NAME=Snapshot Test", "GIT_COMMITTER_EMAIL=snapshot@example.test")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}
func sourceFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}
func sourceRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	git(t, dir, "init", "-b", "main")
	sourceFile(t, dir, "code.go", "baseline\n")
	sourceFile(t, dir, "deleted.go", "deleted\n")
	sourceFile(t, dir, ".gitignore", "ignored/\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "base")
	head := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-b", "feature")
	return dir, head
}
func capture(t *testing.T, dir, base string) Capture {
	t.Helper()
	captured, err := CaptureWorktree(context.Background(), CaptureOptions{Path: dir, Base: base})
	if err != nil {
		t.Fatal(err)
	}
	return captured
}
func manifest(t *testing.T, snapshot Capture) Manifest {
	t.Helper()
	m, err := Decode(snapshot.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCaptureIncludesStagedUnstagedUntrackedAndDeletions(t *testing.T) {
	dir, head := sourceRepo(t)
	sourceFile(t, dir, "code.go", "staged\n")
	git(t, dir, "add", "code.go")
	sourceFile(t, dir, "code.go", "actual unstaged bytes\r\n")
	sourceFile(t, dir, "nested/é.go", "untracked\n")
	sourceFile(t, dir, "ignored/secret", "excluded\n")
	if err := os.Remove(filepath.Join(dir, "deleted.go")); err != nil {
		t.Fatal(err)
	}
	status := git(t, dir, "status", "--porcelain=v1", "--untracked-files=all")
	index := git(t, dir, "ls-files", "--stage")
	snapshot := capture(t, dir, "")
	if !snapshot.Source.Dirty || snapshot.Source.HeadSHA != head || snapshot.Source.Branch != "feature" {
		t.Fatalf("source: %+v", snapshot.Source)
	}
	m := manifest(t, snapshot)
	if m.BaseRef != head {
		t.Fatalf("wrong default baseline: %s", m.BaseRef)
	}
	entries := map[string]Entry{}
	for _, entry := range m.Target {
		entries[entry.Path] = entry
	}
	if string(m.Blobs[entries["code.go"].Hash]) != "actual unstaged bytes\r\n" {
		t.Fatal("did not capture working file bytes")
	}
	if string(m.Blobs[entries["nested/é.go"].Hash]) != "untracked\n" {
		t.Fatal("missing untracked source")
	}
	for _, name := range []string{"deleted.go", "ignored/secret", ".git/config"} {
		if _, exists := entries[name]; exists {
			t.Fatalf("unexpected target entry %s", name)
		}
	}
	if git(t, dir, "status", "--porcelain=v1", "--untracked-files=all") != status || git(t, dir, "ls-files", "--stage") != index || git(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("capture changed Git state")
	}
}

func TestDirtyCaptureMatchesSameCodeAfterCommit(t *testing.T) {
	dir, base := sourceRepo(t)
	sourceFile(t, dir, "code.go", "same code before and after committing\n")
	sourceFile(t, dir, "new.go", "new file\n")
	dirty := capture(t, dir, "")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "commit reviewed source")
	clean := capture(t, dir, "")
	if !dirty.Source.Dirty || clean.Source.Dirty || dirty.Source.HeadSHA == clean.Source.HeadSHA {
		t.Fatal("wrong commit/dirty metadata")
	}
	if dirty.TargetTreeHash != clean.TargetTreeHash || dirty.BaseTreeHash != clean.BaseTreeHash {
		t.Fatal("tree identity depends on commit metadata")
	}
	if dirty.Digest == clean.Digest {
		t.Fatal("wire digest should include changed source refs")
	}
	if manifest(t, clean).BaseRef != base {
		t.Fatal("clean feature review did not use merge base")
	}
}

func TestCaptureLinkedWorktreeFromSubdirectory(t *testing.T) {
	dir, base := sourceRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, dir, "worktree", "add", "-b", "linked", linked)
	sourceFile(t, linked, "nested/new.go", "linked source\n")
	sourceFile(t, linked, "root.go", "include repository root too\n")
	snapshot := capture(t, filepath.Join(linked, "nested"), base)
	found := false
	for _, entry := range manifest(t, snapshot).Target {
		if entry.Path == "root.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("--path in a subdirectory omitted the rest of the repository")
	}
}

func TestCaptureRejectsUnsupportedOrUnchangedSources(t *testing.T) {
	for _, scenario := range []string{"unchanged", "oversized", "lfs", "sparse", "bad-base"} {
		t.Run(scenario, func(t *testing.T) {
			dir, base := sourceRepo(t)
			sourceFile(t, dir, "code.go", "change\n")
			switch scenario {
			case "unchanged":
				sourceFile(t, dir, "code.go", "baseline\n")
			case "oversized":
				sourceFile(t, dir, "big.go", strings.Repeat("x", MaxFileBytes+1))
			case "lfs":
				sourceFile(t, dir, "pointer", "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 1\n")
			case "sparse":
				git(t, dir, "config", "core.sparseCheckout", "true")
			case "bad-base":
				base = "missing-branch"
			}
			if _, err := CaptureWorktree(context.Background(), CaptureOptions{Path: dir, Base: base}); err == nil {
				t.Fatal("unsupported source accepted")
			}
		})
	}
}

func TestRepositoryIdentityStripsCredentials(t *testing.T) {
	for _, remote := range []string{"git@github.com:doomerlabs/doomer.git", "https://secret:token@github.com/doomerlabs/doomer.git?access_token=secret", "ssh://git@github.com/doomerlabs/doomer.git"} {
		if got := repositoryIdentity(remote); got != "github.com/doomerlabs/doomer" {
			t.Fatalf("identity = %q", got)
		}
	}
	if repositoryIdentity("/some/local/repo.git") != "" {
		t.Fatal("stored a local filesystem remote")
	}
}

func TestTreeHashSharedProtocolVector(t *testing.T) {
	data, err := os.ReadFile("../../testdata/source-tree-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Entries  []Entry `json:"entries"`
		Preimage string  `json:"preimageHex"`
	}
	if err := json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	preimage, err := hex.DecodeString(vector.Preimage)
	if err != nil {
		t.Fatal(err)
	}
	if TreeHash(vector.Entries) != Digest(preimage) {
		t.Fatal("tree hash protocol changed")
	}
	for _, field := range []string{"path", "mode", "hash"} {
		changed := append([]Entry{}, vector.Entries...)
		switch field {
		case "path":
			changed[0].Path = "b.go"
		case "mode":
			changed[0].Mode = "100755"
		case "hash":
			changed[0].Hash = strings.Repeat("0", 64)
		}
		if TreeHash(changed) == TreeHash(vector.Entries) {
			t.Fatalf("hash ignores %s", field)
		}
	}
}

func TestWorkspaceCapturesLargeRepositoryWithoutChangingEmbeddedLimits(t *testing.T) {
	dir, _ := sourceRepo(t)
	for _, name := range []string{"one.bin", "two.bin", "three.bin"} {
		sourceFile(t, dir, name, strings.Repeat(name[:1], 6<<20))
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "larger repository")
	base := git(t, dir, "rev-parse", "HEAD")
	sourceFile(t, dir, "code.go", "changed\n")
	captured, err := CaptureWorktree(context.Background(), CaptureOptions{Path: dir, Base: base, Workspace: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Bytes) <= MaxBytes {
		t.Fatal("test must exceed the embedded-upload limit")
	}
	m, err := DecodeWorkspace(captured.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Target) != 6 {
		t.Fatalf("lost repository context: %d files", len(m.Target))
	}
	if _, err := Decode(captured.Bytes); err == nil {
		t.Fatal("embedded decoder accepted a workspace-sized payload")
	}
	if _, err := CaptureWorktree(context.Background(), CaptureOptions{Path: dir, Base: base}); err == nil {
		t.Fatal("embedded capture limits changed")
	}
}

func TestTreeSizePreflightRejectsOversizedBlobBeforeLoadingContent(t *testing.T) {
	dir, _ := sourceRepo(t)
	sourceFile(t, dir, "large.bin", strings.Repeat("a", (2<<20)+1))
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "oversized blob")
	blobs := map[string][]byte{}
	_, err := (gitReader{ctx: context.Background(), dir: dir}).readTree("HEAD", blobs)
	if err == nil || !strings.Contains(err.Error(), "baseline file exceeds") {
		t.Fatalf("expected preflight rejection: %v", err)
	}
	if len(blobs) != 0 {
		t.Fatal("loaded content before rejecting oversized tree")
	}
}
func TestTreeSizePreflightAggregateAndFraming(t *testing.T) {
	for _, data := range []string{"a blob 6\nb blob 6\n", "a blob 11\nb blob 0\n", "a tree 1\nb blob 1\n", "b blob 1\na blob 1\n", "a blob 1\n"} {
		if err := validateTreeSizes([]byte(data), []string{"a", "b"}, 10, 10); err == nil {
			t.Fatalf("accepted invalid or oversized tree %q", data)
		}
	}
	if err := validateTreeSizes([]byte("a blob 6\nb blob 4\n"), []string{"a", "b"}, 10, 10); err != nil {
		t.Fatal(err)
	}
}

func TestSkillSymlinksDoNotBlockWorktreeCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary")
	}
	for _, scenario := range []string{"installer alias", "other target", "tracked alias", "ordinary directory"} {
		t.Run(scenario, func(t *testing.T) {
			dir, base := sourceRepo(t)
			sourceFile(t, dir, ".agents/skills/doomer-review/SKILL.md", "canonical skill\n")
			alias := filepath.Join(dir, ".claude/skills/doomer-review")
			if err := os.MkdirAll(filepath.Dir(alias), 0755); err != nil {
				t.Fatal(err)
			}
			target := "../../.agents/skills/doomer-review"
			if scenario == "other target" {
				target = "../../outside"
			}
			if scenario == "ordinary directory" {
				sourceFile(t, dir, ".claude/skills/doomer-review/SKILL.md", "real source\n")
			} else if err := os.Symlink(target, alias); err != nil {
				t.Fatal(err)
			}
			if scenario == "tracked alias" {
				git(t, dir, "add", ".")
				git(t, dir, "commit", "-m", "tracked link")
			}
			sourceFile(t, dir, "code.go", "new code\n")
			captured, err := CaptureWorktree(context.Background(), CaptureOptions{Path: dir, Base: base, Workspace: true})
			if err != nil {
				t.Fatal(err)
			}
			m, err := DecodeWorkspace(captured.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			found := map[string]bool{}
			for _, entry := range m.Target {
				found[entry.Path] = true
				if scenario != "ordinary directory" && strings.HasPrefix(entry.Path, ".claude/skills/doomer-review/") {
					t.Fatalf("followed installer alias into %q", entry.Path)
				}
			}
			if !found["code.go"] || !found[".agents/skills/doomer-review/SKILL.md"] {
				t.Fatal("omitted actual source or canonical skill")
			}
			if found[".claude/skills/doomer-review"] {
				t.Fatal("captured installer symlink")
			}
			if scenario == "ordinary directory" && !found[".claude/skills/doomer-review/SKILL.md"] {
				t.Fatal("omitted regular directory")
			}
		})
	}
}

func TestCaptureSkipsSymlinksInHistoryAndWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary")
	}
	for _, tracked := range []bool{false, true} {
		t.Run(fmt.Sprintf("tracked=%t", tracked), func(t *testing.T) {
			dir, base := sourceRepo(t)
			outside := t.TempDir()
			sourceFile(t, outside, "secret", strings.Repeat("x", MaxFileBytes+1))
			links := map[string]string{
				"file-link":          "code.go",
				"external-file":      filepath.Join(outside, "secret"),
				"external-directory": outside,
				"broken-link":        "missing-target",
				"directory-link":     ".agents/skills",
			}
			sourceFile(t, dir, ".agents/skills/example/SKILL.md", "regular skill\n")
			for name, target := range links {
				if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			if tracked {
				git(t, dir, "add", ".")
				git(t, dir, "commit", "-m", "links in baseline")
				base = git(t, dir, "rev-parse", "HEAD")
			}
			sourceFile(t, dir, "code.go", "changed regular file\n")
			status := git(t, dir, "status", "--porcelain=v1", "--untracked-files=all")
			for _, workspace := range []bool{false, true} {
				captured, err := CaptureWorktree(context.Background(), CaptureOptions{Path: dir, Base: base, Workspace: workspace})
				if err != nil {
					t.Fatal(err)
				}
				m, err := DecodeWorkspace(captured.Bytes)
				if err != nil {
					t.Fatal(err)
				}
				for _, tree := range [][]Entry{m.Base, m.Target} {
					for _, entry := range tree {
						for link := range links {
							if entry.Path == link || strings.HasPrefix(entry.Path, link+"/") {
								t.Fatalf("captured symlink %q", entry.Path)
							}
						}
					}
				}
				if !bytes.Contains(captured.Bytes, []byte(".agents/skills/example/SKILL.md")) {
					t.Fatal("omitted regular skill")
				}
				baseline, _, err := CaptureBaseline(context.Background(), CaptureOptions{Path: dir, Base: base})
				if err != nil || TreeHash(baseline.Base) != captured.BaseTreeHash {
					t.Fatalf("baseline differs from review capture: %v", err)
				}
			}
			if got := git(t, dir, "status", "--porcelain=v1", "--untracked-files=all"); got != status {
				t.Fatal("capture changed repository state")
			}
		})
	}
}

func TestCaptureSkipsTrackedPathsUnderSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary")
	}
	dir, _ := sourceRepo(t)
	sourceFile(t, dir, "nested/source.go", "original\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "tracked directory")
	base := git(t, dir, "rev-parse", "HEAD")
	if err := os.RemoveAll(filepath.Join(dir, "nested")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sourceFile(t, outside, "source.go", "must not be read\n")
	if err := os.Symlink(outside, filepath.Join(dir, "nested")); err != nil {
		t.Fatal(err)
	}
	m := manifest(t, capture(t, dir, base))
	for _, entry := range m.Target {
		if strings.HasPrefix(entry.Path, "nested") {
			t.Fatalf("captured path beneath symlink: %s", entry.Path)
		}
	}
}

func TestCaptureIncludesRegularFileReplacingTrackedSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary")
	}
	dir, _ := sourceRepo(t)
	link := filepath.Join(dir, "link.go")
	if err := os.Symlink("code.go", link); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "tracked link")
	base := git(t, dir, "rev-parse", "HEAD")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	sourceFile(t, dir, "link.go", "regular replacement\n")
	m := manifest(t, capture(t, dir, base))
	for _, entry := range m.Target {
		if entry.Path == "link.go" && string(m.Blobs[entry.Hash]) == "regular replacement\n" {
			return
		}
	}
	t.Fatal("omitted regular replacement")
}

func TestCaptureStillRejectsSubmodules(t *testing.T) {
	dir, base := sourceRepo(t)
	git(t, dir, "update-index", "--add", "--cacheinfo", "160000", base, "module")
	if _, err := CaptureWorktree(context.Background(), CaptureOptions{Path: dir, Base: base}); err == nil || !strings.Contains(err.Error(), "submodules are unsupported") {
		t.Fatalf("expected unsupported submodule: %v", err)
	}
	git(t, dir, "commit", "-m", "submodule")
	if _, _, err := CaptureBaseline(context.Background(), CaptureOptions{Path: dir, Base: "HEAD"}); err == nil || !strings.Contains(err.Error(), "submodules are unsupported") {
		t.Fatalf("expected unsupported baseline submodule: %v", err)
	}
}

func TestSymlinkChangesDoNotCreateReviewChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink permissions vary")
	}
	dir, _ := sourceRepo(t)
	link := filepath.Join(dir, "link")
	if err := os.Symlink("code.go", link); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "tracked link")
	base := git(t, dir, "rev-parse", "HEAD")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", link); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureWorktree(context.Background(), CaptureOptions{Path: dir, Base: base}); err == nil || !strings.Contains(err.Error(), "snapshot contains no changes") {
		t.Fatalf("link change should not produce a review: %v", err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", "changed link target")
	if _, err := CaptureWorktree(context.Background(), CaptureOptions{Path: dir, Base: base}); err == nil || !strings.Contains(err.Error(), "snapshot contains no changes") {
		t.Fatalf("committed link change should not produce a review: %v", err)
	}
}

func TestLocalReviewIdentityUsesRootNameAndKeepsDirectoriesDistinct(t *testing.T) {
	dir, base := sourceRepo(t)
	sourceFile(t, dir, "nested/change.go", "change\n")
	root := capture(t, dir, base)
	nested := capture(t, filepath.Join(dir, "nested"), base)
	want := LocalRepositoryIdentifier(dir)
	if root.Source.Repository != want || nested.Source.Repository != want {
		t.Fatalf("inconsistent local repository: root=%q nested=%q want=%q", root.Source.Repository, nested.Source.Repository, want)
	}
	_, baselineRepo, err := CaptureBaseline(context.Background(), CaptureOptions{Path: filepath.Join(dir, "nested"), Base: base})
	if err != nil || baselineRepo != want {
		t.Fatalf("baseline identity = %q, %v; want %q", baselineRepo, err, want)
	}
	first := LocalRepositoryIdentifier(filepath.Join(t.TempDir(), "My App"))
	second := LocalRepositoryIdentifier(filepath.Join(t.TempDir(), "My App"))
	if first == second || !strings.HasPrefix(first, "local/My%20App/") {
		t.Fatalf("local names or directory separation lost: %q, %q", first, second)
	}
	git(t, dir, "remote", "add", "origin", "git@github.com:org/my-app.git")
	if got := capture(t, dir, base).Source.Repository; got != "github.com/org/my-app" {
		t.Fatalf("remote identity did not take precedence: %q", got)
	}
	_, baselineRepo, err = CaptureBaseline(context.Background(), CaptureOptions{Path: dir, Base: base})
	if err != nil || baselineRepo != "github.com/org/my-app" {
		t.Fatalf("remote baseline identity = %q, %v", baselineRepo, err)
	}
}

func TestLocalReviewIdentityIgnoresPathSpelling(t *testing.T) {
	dir, base := sourceRepo(t)
	sourceFile(t, dir, "nested/change.go", "change\n")
	want := LocalRepositoryIdentifier(dir)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := LocalRepositoryIdentifier(relative); got != want {
		t.Fatalf("relative identity = %q, want %q", got, want)
	}
	if got := capture(t, filepath.Join(dir, "nested"), base).Source.Repository; got != want {
		t.Fatalf("nested capture identity = %q, want %q", got, want)
	}
	t.Run("symlink", func(t *testing.T) {
		alias := filepath.Join(t.TempDir(), "other-name")
		if err := os.Symlink(dir, alias); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got := LocalRepositoryIdentifier(alias); got != want {
			t.Fatalf("symlink identity = %q, want %q", got, want)
		}
		if got := capture(t, filepath.Join(alias, "nested"), base).Source.Repository; got != want {
			t.Fatalf("symlinked nested capture identity = %q, want %q", got, want)
		}
	})
}
