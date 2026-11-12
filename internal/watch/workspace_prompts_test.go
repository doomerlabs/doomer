package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func nestedRepository(t *testing.T, parent, relative string) string {
	t.Helper()
	source := repository(t)
	target := filepath.Join(parent, relative)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, target); err != nil {
		t.Fatal(err)
	}
	return target
}

func workspaceHook(t *testing.T, root, directory, session, event string, cfg Config) {
	t.Helper()
	raw, _ := json.Marshal(HookInput{Event: event, Session: session, Turn: "turn", CWD: directory, Prompt: "edit a nested project"})
	var output bytes.Buffer
	hookWithReadiness(context.Background(), root, cfg, bytes.NewReader(raw), &output, func(context.Context) bool { return false }, "codex")
	if bytes.Contains(output.Bytes(), []byte("could not")) {
		t.Fatal(output.String())
	}
}

func TestDiscoverRepositoriesRecursesAndIgnoresSymlinkEscapes(t *testing.T) {
	parent := t.TempDir()
	deep := nestedRepository(t, parent, "a/b/c/d/project")
	other := nestedRepository(t, parent, "another/project")
	external := repository(t)
	if err := os.Symlink(external, filepath.Join(parent, "external-link")); err != nil {
		t.Fatal(err)
	}
	repos, err := DiscoverRepositories(context.Background(), parent)
	if err != nil || len(repos) != 2 {
		t.Fatalf("discovery = %v, %v", repos, err)
	}
	got := map[string]bool{}
	for _, repo := range repos {
		got[repo] = true
	}
	if !got[deep] || !got[other] || got[external] {
		t.Fatal(repos)
	}
}

func TestWorkspacePromptReviewsOnlyChangedNestedRepository(t *testing.T) {
	root, parent := t.TempDir(), t.TempDir()
	changed := nestedRepository(t, parent, "several/levels/deep/project")
	untouched := nestedRepository(t, parent, "sibling")
	for _, repo := range []string{changed, untouched} {
		if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("existing edit before prompt\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{AllChanges: true, PromptScoped: true, AllRepositories: true, API: "https://example.com/api"}
	workspaceHook(t, root, parent, "session", "UserPromptSubmit", cfg)
	client := &fakeClient{}
	runner := Runner{Root: root, Config: cfg, Client: client, Token: "test", Output: io.Discard}
	for _, repo := range []string{changed, untouched} {
		if err := runner.Run(context.Background(), repo, true); err != nil {
			t.Fatal(err)
		}
	}
	if client.starts != 0 {
		t.Fatal("uploaded pre-existing sibling edits")
	}
	if err := os.WriteFile(filepath.Join(changed, "file.txt"), []byte("changed during prompt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), changed, true); err != nil {
		t.Fatal(err)
	}
	if client.starts != 1 {
		t.Fatalf("review submissions: %d", client.starts)
	}
	var turn Turn
	if err := Read(TurnPath(root, "session"), &turn); err != nil {
		t.Fatal(err)
	}
	if len(turn.Repositories) != 1 || turn.Repositories[0] != changed {
		t.Fatalf("wrong review targets: %v", turn.Repositories)
	}
	workspaceHook(t, root, parent, "session", "Stop", cfg)
	scopes, err := WorkspacePrompts(root)
	if err != nil || len(scopes) != 0 {
		t.Fatalf("turn scope survived Stop: %v %v", scopes, err)
	}
	if err := os.WriteFile(filepath.Join(changed, "file.txt"), []byte("edit after stop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), changed, true); err != nil {
		t.Fatal(err)
	}
	if client.starts != 1 {
		t.Fatal("continued submitting after Stop")
	}
}

func TestWorkspaceDiscoversRepositoryCreatedDuringPrompt(t *testing.T) {
	root, parent := t.TempDir(), t.TempDir()
	cfg := Config{AllChanges: true, PromptScoped: true, AllRepositories: true, API: "https://example.com/api"}
	workspaceHook(t, root, parent, "session", "UserPromptSubmit", cfg)
	repo := nestedRepository(t, parent, "new/nested/repository")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("new change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	active, count, err := RefreshWorkspacePrompts(context.Background(), root, cfg)
	if err != nil || count != 1 || !active[repo] {
		t.Fatalf("new repository not discovered: %v %d %v", active, count, err)
	}
	client := &fakeClient{}
	if err := (Runner{Root: root, Config: cfg, Client: client, Token: "test"}).Run(context.Background(), repo, true); err != nil {
		t.Fatal(err)
	}
	if client.starts != 1 {
		t.Fatal("new repository did not submit")
	}
}

func TestWorkspaceStopDrainsFinalSnapshotBeforeExiting(t *testing.T) {
	root, parent := t.TempDir(), t.TempDir()
	repo := nestedRepository(t, parent, "deep/repository")
	cfg := Config{AllChanges: true, PromptScoped: true, AllRepositories: true, API: "https://example.com/api"}
	workspaceHook(t, root, parent, "session", "UserPromptSubmit", cfg)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("final turn edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspaceHook(t, root, parent, "session", "Stop", cfg)
	if scopes, err := WorkspacePrompts(root); err != nil || len(scopes) != 1 {
		t.Fatalf("queued review lost at Stop: %v %v", scopes, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("later unrelated edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{}
	if err := (Runner{Root: root, Config: cfg, Client: client, Token: "test"}).Run(context.Background(), repo, true); err != nil {
		t.Fatal(err)
	}
	if client.starts != 1 || !strings.Contains(client.input.Diff, "final turn edit") || strings.Contains(client.input.Diff, "later unrelated edit") {
		t.Fatal("did not drain the final turn snapshot")
	}
	if scopes, err := WorkspacePrompts(root); err != nil || len(scopes) != 0 {
		t.Fatalf("completed turn kept watching: %v %v", scopes, err)
	}
}

func TestWorkspaceDiscoveryHonorsApprovalAndConcurrentTurns(t *testing.T) {
	root, parent := t.TempDir(), t.TempDir()
	allowed := nestedRepository(t, parent, "allowed")
	denied := nestedRepository(t, parent, "denied")
	cfg := Config{AllChanges: true, PromptScoped: true, Directories: []string{allowed}, API: "https://example.com/api"}
	workspaceHook(t, root, parent, "one", "UserPromptSubmit", cfg)
	workspaceHook(t, root, parent, "two", "UserPromptSubmit", cfg)
	workspaceHook(t, root, parent, "one", "Stop", cfg)
	active, count, err := RefreshWorkspacePrompts(context.Background(), root, cfg)
	if err != nil || count != 1 || !active[allowed] || active[denied] {
		t.Fatalf("approval or other turn lost: %v %d %v", active, count, err)
	}
	if err := os.WriteFile(filepath.Join(denied, "file.txt"), []byte("not approved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if queued, err := (Runner{Root: root, Config: cfg}).queueAllChanges(context.Background(), denied, testCapture(t, denied)); err != nil || queued {
		t.Fatalf("unapproved repository queued: %t %v", queued, err)
	}
}

func testCapture(t *testing.T, repo string) sourcesnapshot.Capture {
	t.Helper()
	c, err := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Path: repo, Workspace: true})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
