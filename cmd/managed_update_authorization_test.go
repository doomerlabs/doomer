package cmd

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
)

func updateApprovalTask(t *testing.T, repo string) managed.Task {
	t.Helper()
	id, err := watch.RepositoryIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	return managed.Task{Repo: repo, RepositoryID: id, API: "https://doomer.ai/api", Profile: "default", Bootstrap: managed.Bootstrap{
		Path: filepath.Join(repo, ".agents/skills/doomer-review/SKILL.md"), Scope: "project", Policy: managed.Policy{Protocol: managed.Protocol, Channel: "stable"},
	}}
}

func TestUpdateApprovalIsSharedAcrossLinkedWorktreesAndAgents(t *testing.T) {
	isolate(t)
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	repo := newWatchTestRepo(t)
	linked := filepath.Join(t.TempDir(), "branch")
	if output, err := exec.Command("git", "-C", repo, "-c", "core.hooksPath=/dev/null", "worktree", "add", "-b", "approval-test", linked).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", output, err)
	}
	first, second := updateApprovalTask(t, repo), updateApprovalTask(t, linked)
	second.Bootstrap.Path = filepath.Join(linked, ".claude/skills/doomer-review/SKILL.md")
	if first.RepositoryID != second.RepositoryID {
		t.Fatal("fixture is not linked")
	}
	if allowed, err := authorizeManagedUpdates(context.Background(), s, first, true, selectedRelease{}); err != nil || !allowed {
		t.Fatal(allowed, err)
	}
	if allowed, err := authorizeManagedUpdates(context.Background(), s, second, false, selectedRelease{}); err != nil || !allowed {
		t.Fatal("asked again for linked checkout", allowed, err)
	}
	for _, changed := range []string{"repository", "service", "profile", "policy"} {
		other := second
		switch changed {
		case "repository":
			other = updateApprovalTask(t, newWatchTestRepo(t))
		case "service":
			other.API = "https://another.example/api"
		case "profile":
			other.Profile = "another"
		case "policy":
			other.Bootstrap.Policy = managed.Policy{Protocol: managed.Protocol, Channel: "pinned", Version: "2026.11.6.9"}
		}
		if allowed, err := authorizeManagedUpdates(context.Background(), s, other, false, selectedRelease{}); err != nil || allowed {
			t.Fatal("crossed scope", changed, allowed, err)
		}
	}
}

func TestLegacyApprovalMigratesFromOriginalCheckoutWithMatchingEvidence(t *testing.T) {
	isolate(t)
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	first := updateApprovalTask(t, newWatchTestRepo(t))
	first.Release = "2026.11.6.9"
	first.Handle, _ = managed.NewHandle()
	first.Schema = managed.Protocol
	if err := s.Save(&first); err != nil {
		t.Fatal(err)
	}
	selection := selectedRelease{Authorized: true, Policy: first.Bootstrap.Policy}
	if err := watch.Write(filepath.Join(s.Root, "selections", watch.Key(first.Bootstrap.Path)+".json"), selection); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Bootstrap.Path = filepath.Join(t.TempDir(), ".claude/skills/doomer-review/SKILL.md")
	if allowed, err := authorizeManagedUpdates(context.Background(), s, second, false, selectedRelease{}); err != nil || !allowed {
		t.Fatal("legacy approval not reused", allowed, err)
	}
	path := filepath.Join(s.Root, "update-authorizations", updateAuthorizationKey(second)+".json")
	if err := watch.Write(path, updateAuthorization{Authorized: false}); err != nil {
		t.Fatal(err)
	}
	if allowed, err := authorizeManagedUpdates(context.Background(), s, second, false, selection); err != nil || allowed {
		t.Fatal("resurrected revoked authorization", allowed, err)
	}
}
