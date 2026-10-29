package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	watching "github.com/doomerlabs/doomer/internal/watch"
)

func TestInitialWatchDirectoriesIncludesApprovedCurrentCheckout(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	root, err := watching.Root(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []watching.Config{{AllRepositories: true}, {Directories: []string{root}}} {
		got := initialWatchDirectories(context.Background(), cfg, nested)
		if len(got) != 1 || got[0] != root {
			t.Fatalf("current checkout not included once: %v", got)
		}
	}
	// Approval for another repository/profile must never cover this checkout.
	cfg := watching.Config{Directories: []string{"/approved/other"}}
	got := initialWatchDirectories(context.Background(), cfg, root)
	if len(got) != 1 || got[0] != "/approved/other" {
		t.Fatalf("unapproved checkout added: %v", got)
	}
}

func TestInitialWatchDirectoriesOutsideRepository(t *testing.T) {
	cfg := watching.Config{AllRepositories: true}
	if got := initialWatchDirectories(context.Background(), cfg, t.TempDir()); len(got) != 0 {
		t.Fatal(got)
	}
}
