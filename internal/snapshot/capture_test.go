package snapshot

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", root}, args...)...)
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func write(t *testing.T, root, p, content string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(root, p), []byte(content), 0644); e != nil {
		t.Fatal(e)
	}
}
func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testGit(t, root, "init", "--template=")
	write(t, root, "code.go", "original\n")
	write(t, root, "deleted.go", "delete me\n")
	write(t, root, ".gitignore", "ignored*\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "initial")
	return root
}
func TestCaptureFreezesWorktreeAndPreservesIndex(t *testing.T) {
	root := repo(t)
	write(t, root, "code.go", "staged\n")
	testGit(t, root, "add", "code.go")
	write(t, root, "code.go", "unstaged\n")
	write(t, root, "new.go", "untracked\n")
	write(t, root, "ignored.txt", "secret\n")
	os.Remove(filepath.Join(root, "deleted.go"))
	indexBefore, e := os.ReadFile(filepath.Join(root, ".git", "index"))
	if e != nil {
		t.Fatal(e)
	}
	head := testGit(t, root, "rev-parse", "HEAD")
	data, e := Capture(context.Background(), Options{Path: root})
	if e != nil {
		t.Fatal(e)
	}
	m, e := Decode(data)
	if e != nil {
		t.Fatal(e)
	}
	got := map[string]string{}
	for _, entry := range m.Target {
		got[entry.Path] = string(m.Blobs[entry.Hash])
	}
	if got["code.go"] != "unstaged\n" || got["new.go"] != "untracked\n" {
		t.Fatalf("wrong target: %v", got)
	}
	for _, p := range []string{"deleted.go", "ignored.txt"} {
		if _, ok := got[p]; ok {
			t.Fatalf("included %s", p)
		}
	}
	indexAfter, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	if !bytes.Equal(indexBefore, indexAfter) || head != testGit(t, root, "rev-parse", "HEAD") {
		t.Fatal("capture changed user Git state")
	}
	write(t, root, "code.go", "later\n")
	os.RemoveAll(root)
	if string(m.Blobs[m.Target[1].Hash]) != "unstaged\n" {
		t.Fatal("snapshot changed after worktree removal")
	}
}
func TestStagedAndBranchScopes(t *testing.T) {
	root := repo(t)
	base := testGit(t, root, "rev-parse", "HEAD")
	write(t, root, "code.go", "committed\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "branch")
	write(t, root, "code.go", "staged\n")
	testGit(t, root, "add", "code.go")
	write(t, root, "code.go", "unstaged\n")
	write(t, root, "new.go", "new\n")
	for _, tc := range []struct {
		o     Options
		want  string
		count int
	}{{Options{Path: root, Staged: true}, "staged\n", 3}, {Options{Path: root, Base: base}, "unstaged\n", 4}} {
		b, e := Capture(context.Background(), tc.o)
		if e != nil {
			t.Fatal(e)
		}
		m, e := Decode(b)
		if e != nil {
			t.Fatal(e)
		}
		if len(m.Target) != tc.count {
			t.Fatalf("target count %d", len(m.Target))
		}
		for _, entry := range m.Target {
			if entry.Path == "code.go" && string(m.Blobs[entry.Hash]) != tc.want {
				t.Fatal("wrong scope contents")
			}
		}
	}
}
func TestCaptureRejectsUnsupportedAndEmptySources(t *testing.T) {
	root := repo(t)
	if _, e := Capture(context.Background(), Options{Path: root}); e == nil {
		t.Fatal("accepted empty diff")
	}
	if e := os.Symlink("code.go", filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e := Capture(context.Background(), Options{Path: root}); e == nil {
		t.Fatal("accepted symlink")
	}
	os.Remove(filepath.Join(root, "link"))
	write(t, root, "new.go", "version https://git-lfs.github.com/spec/v1\n")
	if _, e := Capture(context.Background(), Options{Path: root}); e == nil {
		t.Fatal("accepted LFS pointer")
	}
}

func TestCleanFeatureBranchDefaultsToMainMergeBase(t *testing.T) {
	root := repo(t)
	testGit(t, root, "branch", "-M", "main")
	testGit(t, root, "checkout", "-b", "feature")
	write(t, root, "code.go", "branch change\n")
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", "change")
	b, e := Capture(context.Background(), Options{Path: root})
	if e != nil {
		t.Fatal(e)
	}
	m, e := Decode(b)
	if e != nil || m.Scope != "branch" {
		t.Fatalf("scope: %+v %v", m, e)
	}
	testGit(t, root, "checkout", "main")
	if _, e = Capture(context.Background(), Options{Path: root}); e == nil {
		t.Fatal("reviewed clean default branch")
	}
}
