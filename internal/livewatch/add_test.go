package livewatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAddDirectoryTargetsOneWatcherAndPreservesPendingChanges(t *testing.T) {
	root := t.TempDir()
	sibling := makeRepository(t, root, "sibling")
	if err := os.WriteFile(filepath.Join(sibling, "file.txt"), []byte("preexisting\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var configs []Config
	var dones []<-chan error
	clients := []*fakeClient{{}, {}, {}, {}}
	for i := range clients {
		repo := makeRepository(t, t.TempDir(), "primary")
		cfg, done := startWorker(t, repo, clients[i], time.Second, func(context.Context, string) bool { return true })
		configs = append(configs, cfg)
		dones = append(dones, done)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	selected := configs[2]
	if _, err := AddDirectory(ctx, selected.ControlKey, sibling); err != nil {
		t.Fatal(err)
	}
	result, err := Call(ctx, selected.ControlKey, "checkpoint")
	if err != nil || len(result.Results) != 0 {
		t.Fatalf("registration reviewed old edits: %+v %v", result, err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "file.txt"), []byte("new edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Re-adding before a scan must not discard the new edit.
	if _, err := AddDirectory(ctx, selected.ControlKey, sibling); err != nil {
		t.Fatal(err)
	}
	// Nor may adding an overlapping root reset existing repository state.
	if _, err := AddDirectory(ctx, selected.ControlKey, root); err != nil {
		t.Fatal(err)
	}
	for i, cfg := range configs {
		result := stopWorker(t, cfg, dones[i])
		if i == 2 {
			if len(result.Results) != 1 || result.Results[0].Repository != sibling || result.Results[0].Status != "ready" {
				t.Fatalf("selected watcher: %+v", result)
			}
		} else if len(result.Results) != 0 {
			t.Fatalf("unrelated watcher %d reviewed sibling: %+v", i, result)
		}
	}
}

func TestAddDirectoryRequiresPermissionAndValidDirectory(t *testing.T) {
	primary := makeRepository(t, t.TempDir(), "primary")
	denied := makeRepository(t, t.TempDir(), "denied")
	cfg, done := startWorker(t, primary, &fakeClient{}, time.Second, func(_ context.Context, path string) bool { return path != denied })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, path := range []string{denied, "relative", filepath.Join(primary, "file.txt"), t.TempDir(), filepath.Join(primary, "missing")} {
		if _, err := AddDirectory(ctx, cfg.ControlKey, path); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	if _, err := AddDirectory(ctx, Key("another-session"), primary); err == nil {
		t.Fatal("accepted nonexistent watcher")
	}
	if err := os.WriteFile(filepath.Join(denied, "file.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := stopWorker(t, cfg, done); len(result.Results) != 0 {
		t.Fatalf("denied registration changed scope: %+v", result)
	}
}
