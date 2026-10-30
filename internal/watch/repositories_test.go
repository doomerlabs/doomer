package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEditHookReactivatesDormantCheckoutButNotRemovedCheckout(t *testing.T) {
	root, path := t.TempDir(), repository(t)
	cfg := Config{AllRepositories: true}
	if err := RegisterRepository(root, path, "agent", false, time.Now().Add(-15*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	owner := Owner{Session: "chat", Turn: "turn"}
	if err := Write(TurnPath(root, owner.Session), Turn{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	input := HookInput{Session: owner.Session, Turn: owner.Turn, CWD: path, Event: "PreToolUse", Tool: "Edit", ToolID: "edit-1"}
	data, _ := json.Marshal(input)
	var output strings.Builder
	hookForTest(context.Background(), root, cfg, strings.NewReader(string(data)), &output)
	repos, err := Repositories(root, cfg)
	if err != nil || len(repos) != 1 || !repos[0].Active(time.Now()) {
		t.Fatalf("edit did not reactivate checkout: %+v %v", repos, err)
	}
	if err := RemoveRepository(root, path); err != nil {
		t.Fatal(err)
	}
	hookForTest(context.Background(), root, cfg, strings.NewReader(string(data)), &output)
	if !RepositoryRemoved(root, path) {
		t.Fatal("edit reactivated removed checkout")
	}
}

func TestAutomaticRepositoryExpiresAndAgentEditReactivatesIt(t *testing.T) {
	root, path := t.TempDir(), "/repo"
	now := time.Now()
	if err := RegisterRepository(root, path, "agent", false, now.Add(-15*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	repos, err := Repositories(root, Config{AllRepositories: true})
	if err != nil || len(repos) != 1 || repos[0].Active(now) {
		t.Fatalf("old checkout stayed active: %+v %v", repos, err)
	}
	if err := RegisterRepository(root, path, "agent", false, now); err != nil {
		t.Fatal(err)
	}
	repos, err = Repositories(root, Config{AllRepositories: true})
	if err != nil || !repos[0].Active(now) {
		t.Fatalf("agent did not reactivate checkout: %+v %v", repos, err)
	}
}

func TestExplicitRepositoryDoesNotExpireAndRemovalSurvivesDiscovery(t *testing.T) {
	root, path := t.TempDir(), "/repo"
	now := time.Now()
	if err := RegisterRepository(root, path, "explicit", true, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	repos, _ := Repositories(root, Config{})
	if len(repos) != 1 || !repos[0].Active(now) {
		t.Fatal("explicit checkout expired")
	}
	if err := RemoveRepository(root, path); err != nil {
		t.Fatal(err)
	}
	if err := RegisterRepository(root, path, "agent", false, now); err != nil {
		t.Fatal(err)
	}
	if !RepositoryRemoved(root, path) {
		t.Fatal("hook revived removed checkout")
	}
	if err := RegisterRepository(root, path, "explicit", true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if RepositoryRemoved(root, path) {
		t.Fatal("explicit add failed to restore checkout")
	}
}

func TestLegacyDiscoveryKeepsAgeAndSupportsMoreThanSixteenCheckouts(t *testing.T) {
	root := t.TempDir()
	old := time.Now().Add(-15 * 24 * time.Hour)
	for i := 0; i < 20; i++ {
		path := fmt.Sprintf("/repo-%d", i)
		file := filepath.Join(root, "discovered", Key(path)+".json")
		if err := Write(file, path); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(file, old, old); err != nil {
			t.Fatal(err)
		}
	}
	repos, err := Repositories(root, Config{AllRepositories: true})
	if err != nil || len(repos) != 20 {
		t.Fatalf("lost checkouts: %d %v", len(repos), err)
	}
	for _, r := range repos {
		if r.Active(time.Now()) {
			t.Fatal("migration renewed old discovery")
		}
	}
	// Another linked-worktree scan must not renew the inactivity clock.
	if err := RegisterRepository(root, repos[0].Path, "linked worktree", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	repos, _ = Repositories(root, Config{AllRepositories: true})
	if repos[0].Active(time.Now()) {
		t.Fatal("linked discovery reactivated idle checkout")
	}
}

func TestRepositorySelectionsAreProfileScoped(t *testing.T) {
	local, production := t.TempDir(), t.TempDir()
	if err := RemoveRepository(local, "/repo"); err != nil {
		t.Fatal(err)
	}
	if RepositoryRemoved(production, "/repo") {
		t.Fatal("removal leaked into another profile")
	}
}
