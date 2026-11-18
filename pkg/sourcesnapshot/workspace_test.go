package sourcesnapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureWorkspaceWithoutCommit(t *testing.T) {
	for _, repo := range []bool{false, true} {
		name := "plain"
		if repo {
			name = "unborn"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if repo {
				git(t, dir, "init", "-b", "main")
			}
			sourceFile(t, dir, ".gitignore", "ignored/\n")
			sourceFile(t, dir, "code.go", "before\n")
			sourceFile(t, dir, "ignored/secret", "secret\n")
			if repo {
				git(t, dir, "add", "code.go")
			}
			before := captureWorkspace(t, dir, nil)
			m := manifest(t, before)
			if len(m.Base) != 0 || m.HeadRef != "" || len(m.Target) != 2 {
				t.Fatalf("unexpected initial snapshot: %+v", m)
			}
			if !repo {
				if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
					t.Fatal("capture created Git metadata")
				}
			}
			sourceFile(t, dir, "code.go", "after\n")
			after := captureWorkspace(t, dir, &m)
			edited := manifest(t, after)
			if len(edited.Base) != 2 || edited.Base[1].Hash == edited.Target[1].Hash {
				t.Fatal("lost the initial contents")
			}
			if err := os.Remove(filepath.Join(dir, "code.go")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(dir, ".gitignore")); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(filepath.Join(dir, "ignored")); err != nil {
				t.Fatal(err)
			}
			deleted := manifest(t, captureWorkspace(t, dir, &m))
			if len(deleted.Base) != 2 || len(deleted.Target) != 0 {
				t.Fatalf("lost deletion: %+v", deleted)
			}
		})
	}
}

func captureWorkspace(t *testing.T, dir string, initial *Manifest) Capture {
	t.Helper()
	c, err := CaptureWorktree(context.Background(), CaptureOptions{Path: dir, Workspace: true, Initial: initial})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWorkspacePreservesTrailingSpaceInRepositoryPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repository ")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "init", "-b", "main")
	sourceFile(t, dir, "code.go", "package example\n")
	snapshot := captureWorkspace(t, dir, nil)
	if snapshot.Source.Repository != LocalRepositoryIdentifier(dir) {
		t.Fatalf("repository path changed: %s", snapshot.Source.Repository)
	}
	m := manifest(t, snapshot)
	if len(m.Target) != 1 || m.Target[0].Path != "code.go" {
		t.Fatalf("captured the wrong source: %+v", m.Target)
	}
}
