package watch

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCancelledWatcherStartupDoesNotWarnAboutFilesystemNotifications(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	runner := Runner{Root: t.TempDir(), Output: &output}
	if err := runner.Run(ctx, t.TempDir(), false); err != nil {
		t.Fatalf("cancelled watcher returned an error: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("cancelled watcher emitted a warning: %s", output.String())
	}
}

func TestFilesystemChangesAndHookRequestsWakeWatcher(t *testing.T) {
	for _, change := range []string{"existing-file", "new-directory", "hook-request"} {
		t.Run(change, func(t *testing.T) {
			dir, root := repository(t), t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			events, err := newFilesystemEvents(ctx, Runner{Root: root, Output: io.Discard}, dir)
			if err != nil {
				t.Fatal(err)
			}
			defer events.watcher.Close()
			switch change {
			case "existing-file":
				err = os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600)
			case "new-directory":
				err = os.Mkdir(filepath.Join(dir, "nested"), 0700)
				if err == nil {
					err = os.WriteFile(filepath.Join(dir, "nested", "new.go"), []byte("package nested\n"), 0600)
				}
			case "hook-request":
				err = Write(StatePath(root, dir), State{RequestedAt: time.Now()})
			}
			if err != nil {
				t.Fatal(err)
			}
			waitCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			defer stop()
			if !events.wait(waitCtx, time.Minute, 0) {
				t.Fatal("filesystem event did not wake capture")
			}
		})
	}
}

func TestFilesystemIgnoresBuildOutputAndDoesNotPollAtIdle(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "build"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := newFilesystemEvents(ctx, Runner{Root: root, Output: io.Discard}, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer events.watcher.Close()
	if err = os.WriteFile(filepath.Join(dir, "build", "output"), []byte("generated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Write(filepath.Join(root, "heartbeat.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	if events.wait(waitCtx, time.Minute, 0) {
		t.Fatal("ignored output or heartbeat woke capture")
	}
}

func TestFilesystemFallbackAndRetryDeadlines(t *testing.T) {
	for _, retry := range []time.Duration{0, 5 * time.Millisecond} {
		var events *filesystemEvents
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if !events.wait(ctx, 10*time.Millisecond, retry) {
			t.Fatal("fallback did not wake capture")
		}
		cancel()
	}
}
