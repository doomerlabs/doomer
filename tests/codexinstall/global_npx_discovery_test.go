package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestNpxSnapshotDetectsPackageMutations(t *testing.T) {
	for _, change := range []string{"content", "mode", "link", "extra-file", "removed-file"} {
		t.Run(change, func(t *testing.T) {
			home := t.TempDir()
			skill := filepath.Join(home, ".agents/skills/doomer-review")
			claude := filepath.Join(home, ".claude/skills/doomer-review")
			if err := os.MkdirAll(skill, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(claude), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../../.agents/skills/doomer-review", claude); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			file := filepath.Join(skill, "SKILL.md")
			if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, ".agents/.skill-lock.json"), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := snapshotNpxInstall(home)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "content":
				err = os.WriteFile(file, []byte("changed"), 0600)
			case "mode":
				err = os.Chmod(file, 0400)
			case "link":
				if err = os.Remove(claude); err == nil {
					err = os.Symlink("/different-target", claude)
				}
			case "extra-file":
				err = os.WriteFile(filepath.Join(skill, "metadata.json"), []byte("added"), 0600)
			case "removed-file":
				err = os.Remove(file)
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := snapshotNpxInstall(home)
			if err == nil && bytes.Equal(before, after) {
				t.Fatalf("did not detect %s mutation", change)
			}
		})
	}
}
