package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestLinkedWorktreeDiscoveryAndConsent(t *testing.T) {
	repo, err := Root(context.Background(), repository(t))
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", repo, "-c", "core.hooksPath=/dev/null"}, args...)...)
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	all := Config{AllRepositories: true}
	if got := LinkedWorktrees(context.Background(), all, []string{repo}); len(got) != 1 {
		t.Fatalf("initial worktrees: %v", got)
	}
	linked := filepath.Join(t.TempDir(), "linked checkout")
	run("worktree", "add", "--detach", linked, "HEAD")
	linked, err = Root(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	got := LinkedWorktrees(context.Background(), all, []string{repo, linked})
	if len(got) != 2 || !slices.Contains(got, repo) || !slices.Contains(got, linked) {
		t.Fatalf("missing or duplicate linked checkout: %v", got)
	}
	scoped := LinkedWorktrees(context.Background(), Config{Directories: []string{repo}}, []string{repo})
	if len(scoped) != 1 || scoped[0] != repo {
		t.Fatalf("directory consent expanded: %v", scoped)
	}
	if got := LinkedWorktrees(context.Background(), Config{}, []string{repo}); len(got) != 0 {
		t.Fatalf("discovery bypassed consent: %v", got)
	}
	if err = os.RemoveAll(linked); err != nil {
		t.Fatal(err)
	}
	got = LinkedWorktrees(context.Background(), all, []string{repo})
	if len(got) != 1 || got[0] != repo {
		t.Fatalf("prunable checkout was discovered: %v", got)
	}
}
