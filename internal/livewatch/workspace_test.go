package livewatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestWatchDirectoryWithoutCommit(t *testing.T) {
	for _, kind := range []string{"plain", "unborn"} {
		for _, remove := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/edit", true: "/delete"}[remove], func(t *testing.T) {
				dir := t.TempDir()
				if kind == "unborn" {
					if out, err := exec.Command("git", "-C", dir, "init", "-b", "main").CombinedOutput(); err != nil {
						t.Fatalf("git init: %s %v", out, err)
					}
				}
				file := filepath.Join(dir, "code.go")
				if err := os.WriteFile(file, []byte("before\n"), 0600); err != nil {
					t.Fatal(err)
				}
				client := &fakeClient{started: make(chan adversarylabs.WorkspaceSubmission, 10)}
				cfg, done := startWorker(t, dir, client, time.Second, func(context.Context, string) bool { return true })
				result, err := Call(context.Background(), cfg.ControlKey, "checkpoint")
				if err != nil || len(result.Results) != 0 {
					t.Fatalf("startup reviewed existing code: %+v %v", result, err)
				}
				if remove {
					err = os.Remove(file)
				} else {
					err = os.WriteFile(file, []byte("after\n"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
				result = stopWorker(t, cfg, done)
				if len(result.Results) != 1 || result.Results[0].Status != "ready" {
					t.Fatalf("review: %+v", result)
				}
				input := <-client.started
				if !strings.Contains(input.Diff, "-before") || (!remove && !strings.Contains(input.Diff, "+after")) {
					t.Fatalf("lost edit baseline: %s", input.Diff)
				}
			})
		}
	}
}

func TestPlainWorkspaceKeepsNestedRepositoriesSeparate(t *testing.T) {
	dir := t.TempDir()
	nested := makeRepository(t, dir, "nested")
	if err := os.WriteFile(filepath.Join(dir, "code.go"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	names, _, err := sourcesnapshot.WorkspaceState(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "code.go" {
		t.Fatalf("nested repository leaked into parent: %v (%s)", names, nested)
	}
}

func TestInitialDirectoryBaselineRejectsOversizedFile(t *testing.T) {
	for _, kind := range []string{"plain", "unborn"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if kind == "unborn" {
				if out, err := exec.Command("git", "-C", dir, "init", "-b", "main").CombinedOutput(); err != nil {
					t.Fatalf("git init: %s %v", out, err)
				}
			}
			file, err := os.Create(filepath.Join(dir, "oversized.bin"))
			if err != nil {
				t.Fatal(err)
			}
			err = file.Truncate(sourcesnapshot.MaxWorkspaceFileBytes + 1)
			closeErr := file.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			baseline, err := initialEditFiles(context.Background(), dir)
			if err == nil || !strings.Contains(err.Error(), "no larger than") || baseline != nil {
				t.Fatalf("oversized baseline was not rejected: %v (files=%d)", err, len(baseline))
			}
		})
	}
}
