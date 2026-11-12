package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

type allChangesClient struct {
	fakeClient
	submitted chan adversarylabs.WorkspaceSubmission
}

func (c *allChangesClient) StartWorkspace(ctx context.Context, token string, input adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	c.submitted <- input
	return c.fakeClient.StartWorkspace(ctx, token, input)
}

type initialScanWriter struct {
	once  sync.Once
	ready chan struct{}
}

func (w *initialScanWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "snapshot contains no changes") {
		w.once.Do(func() { close(w.ready) })
	}
	return len(p), nil
}

func TestAllChangesFilesystemEventSubmitsSettledManualEdit(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	cfg := Config{AllChanges: true, Directories: []string{repo}, API: "https://example.com/api"}
	filesystemPrompt(t, root, repo, cfg)
	client := &allChangesClient{submitted: make(chan adversarylabs.WorkspaceSubmission, 4)}
	output := &initialScanWriter{ready: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	done := make(chan error, 1)
	runner := Runner{Root: root, Config: cfg, Client: client, Token: "test", Interval: time.Hour, Output: output, Debug: true, StartedAt: time.Now().Add(-time.Minute)}
	go func() { done <- runner.Run(ctx, repo, false) }()
	defer func() { cancel(); <-done }()
	select {
	case <-output.ready:
	case <-ctx.Done():
		t.Fatal("watcher did not finish its initial clean scan")
	}
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("intermediate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("settled manual edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case submission := <-client.submitted:
		if !strings.Contains(submission.Diff, "settled manual edit") || strings.Contains(submission.Diff, "+intermediate") {
			t.Fatalf("did not debounce the actual filesystem edit: %s", submission.Diff)
		}
	case <-ctx.Done():
		t.Fatal("manual filesystem edit did not trigger a review")
	}
}

func filesystemPrompt(t *testing.T, root, repo string, cfg Config) {
	t.Helper()
	raw, _ := json.Marshal(HookInput{Event: "UserPromptSubmit", Session: "session", Turn: "turn", CWD: repo, Prompt: "Review this PR"})
	hookWithReadiness(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard, func(context.Context) bool { return false }, "codex")
}

func TestAllChangesReviewsManualEditWithoutToolHooks(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "all-changes"}[enabled], func(t *testing.T) {
			root, repo := t.TempDir(), repository(t)
			cfg := Config{AllChanges: enabled, Directories: []string{repo}, API: "https://example.com/api"}
			filesystemPrompt(t, root, repo, cfg)
			if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("manual edit\n"), 0600); err != nil {
				t.Fatal(err)
			}
			client := &fakeClient{}
			runner := Runner{Root: root, Config: cfg, Client: client, Token: "test", Interval: time.Second}
			if err := runner.Run(context.Background(), repo, true); err != nil {
				t.Fatal(err)
			}
			if !enabled {
				if client.starts != 0 {
					t.Fatal("legacy watcher reviewed an unattributed edit")
				}
				return
			}
			if client.starts != 1 || !strings.Contains(client.input.AgentDiff, "manual edit") || client.input.FullReview == nil || *client.input.FullReview {
				t.Fatalf("manual edit was not reviewed as Quick: %+v", client.input)
			}
			if client.input.Client["agent"] != "filesystem" {
				t.Fatal("filesystem change falsely attributed to an agent")
			}
		})
	}
}

func TestAllChangesDoesNotInventPromptOrRepositoryApproval(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("manual edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	capture, err := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Path: repo, Workspace: true})
	if err != nil {
		t.Fatal(err)
	}
	runner := Runner{Root: root, Config: Config{AllChanges: true, Directories: []string{repo}}}
	if queued, err := runner.queueAllChanges(context.Background(), repo, capture); err != nil || queued {
		t.Fatalf("queued without active prompt: %t %v", queued, err)
	}
	filesystemPrompt(t, root, repo, runner.Config)
	runner.Config.Directories = nil
	if queued, err := runner.queueAllChanges(context.Background(), repo, capture); err != nil || queued {
		t.Fatalf("queued outside approved repository: %t %v", queued, err)
	}
}

func TestAllChangesStopQueuesLatestSourceWithoutToolEvents(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	cfg := Config{AllChanges: true, Directories: []string{repo}}
	filesystemPrompt(t, root, repo, cfg)
	for _, text := range []string{"first edit\n", "later edit\n"} {
		if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(HookInput{Event: "Stop", Session: "session", Turn: "turn", CWD: repo})
		hookWithReadiness(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard, func(context.Context) bool { return false }, "codex")
		var state State
		if err := Read(StatePath(root, repo), &state); err != nil || !strings.Contains(state.AgentDiff, strings.TrimSpace(text)) {
			t.Fatalf("stop did not capture current source: %+v %v", state, err)
		}
		if state.Ambiguous || len(state.Active) != 0 {
			t.Fatal("all-changes required tool ownership")
		}
	}
}
