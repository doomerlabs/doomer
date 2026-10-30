package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRepositorySelectionIncludesLinkedWorktreesButNotOtherRepositories(t *testing.T) {
	main := repository(t)
	linked := filepath.Join(t.TempDir(), "feature")
	if out, err := exec.Command("git", "-C", main, "-c", "core.hooksPath=/dev/null", "worktree", "add", "-b", "feature", linked, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %s %v", out, err)
	}
	linked, err := filepath.EvalSymlinks(linked)
	if err != nil {
		t.Fatal(err)
	}
	id, err := RepositoryIdentity(context.Background(), main)
	if err != nil {
		t.Fatal(err)
	}
	linkedID, err := RepositoryIdentity(context.Background(), linked)
	if err != nil || linkedID != id {
		t.Fatalf("linked identity mismatch: %s %s %v", id, linkedID, err)
	}
	// Existing checkout-only permission does not silently expand.
	if Allowed(Config{Directories: []string{main}}, linked) {
		t.Fatal("old path-only permission expanded")
	}
	cfg := Config{Directories: []string{main}, RepositoryIDs: []string{id}}
	if !Allowed(cfg, linked) || Allowed(cfg, repository(t)) {
		t.Fatal("repository permission crossed Git identities")
	}
	root := t.TempDir()
	if err := RegisterRepository(root, linked, "agent", false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SetRepositorySelection(root, main, true); err != nil {
		t.Fatal(err)
	}
	if !RepositoryRemoved(root, linked) {
		t.Fatal("removing repository left a linked checkout enabled")
	}
	if err := SetRepositorySelection(root, linked, false); err != nil {
		t.Fatal(err)
	}
	if RepositoryRemoved(root, main) {
		t.Fatal("adding through linked checkout did not enable repository")
	}
}

func TestWatchRuntimeRequiresCurrentSessionAndLiveHeartbeat(t *testing.T) {
	root := t.TempDir()
	repo := Repository{Path: "/repo", Explicit: true}
	now := time.Now()
	if got := RepositoryStatus(root, repo, now); got != "Not running" {
		t.Fatal(got)
	}
	if err := Write(filepath.Join(root, "heartbeat.json"), now); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(root, "watch-session.json"), "current"); err != nil {
		t.Fatal(err)
	}
	if err := SetWatchRuntime(root, repo.Path, WatchRuntime{Session: "old", Token: "one", Status: "Watching"}); err != nil {
		t.Fatal(err)
	}
	if got := RepositoryStatus(root, repo, now); got != "Not running" {
		t.Fatal("stale session reported live")
	}
	if err := SetWatchRuntime(root, repo.Path, WatchRuntime{Session: "current", Token: "two", Status: "Watching"}); err != nil {
		t.Fatal(err)
	}
	ClearWatchRuntime(root, repo.Path, "one")
	if got := RepositoryStatus(root, repo, now); got != "Watching" {
		t.Fatal("old worker cleared replacement", got)
	}
	if got := RepositoryStatus(root, repo, now.Add(4*time.Second)); got != "Not running" {
		t.Fatal("stale heartbeat reported live")
	}
	ClearWatchRuntime(root, repo.Path, "two")
	if _, err := os.Stat(filepath.Join(root, "watching", Key(repo.Path)+".json")); !os.IsNotExist(err) {
		t.Fatal("runtime not cleared", err)
	}
}
