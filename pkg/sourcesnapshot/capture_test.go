package sourcesnapshot

import (
	"context"
	"encoding/hex"
	"encoding/json"
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
	for _, scenario := range []string{"unchanged", "symlink", "oversized", "lfs", "sparse", "bad-base"} {
		t.Run(scenario, func(t *testing.T) {
			dir, base := sourceRepo(t)
			sourceFile(t, dir, "code.go", "change\n")
			switch scenario {
			case "unchanged":
				sourceFile(t, dir, "code.go", "baseline\n")
			case "symlink":
				if runtime.GOOS == "windows" {
					t.Skip("symlink permissions vary")
				}
				if err := os.Symlink(filepath.Join(dir, "code.go"), filepath.Join(dir, "link.go")); err != nil {
					t.Fatal(err)
				}
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
