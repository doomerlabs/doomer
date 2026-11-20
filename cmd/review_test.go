package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func reviewGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
	command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Review Test", "GIT_AUTHOR_EMAIL=review@example.test", "GIT_COMMITTER_NAME=Review Test", "GIT_COMMITTER_EMAIL=review@example.test")
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func TestReviewHasNoProjectFlag(t *testing.T) {
	isolate(t)
	_, err := execute(t, "", "review", "--project", "old-project")
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("project selection should be rejected: %v", err)
	}
}

func TestReviewReportsExcludedOnlyWorktreeWithoutSubmitting(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	reviewGit(t, dir, "init", "-b", "main")
	reviewGit(t, dir, "commit", "--allow-empty", "-m", "baseline")
	if err := os.WriteFile(filepath.Join(dir, "asset.png"), []byte("\x00binary"), 0600); err != nil {
		t.Fatal(err)
	}
	command := NewRootCommand()
	var out, diagnostics bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&diagnostics)
	command.SetArgs([]string{"review", "--path", dir, "--format", "json", "--show-skipped"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"status":"unchanged"`) || !strings.Contains(out.String(), "No eligible changes to review.") {
		t.Fatalf("unexpected output: %s", &out)
	}
	if diagnostics.String() != "Skipped \"asset.png\" (binary)\n" {
		t.Fatalf("unexpected diagnostics: %s", &diagnostics)
	}
	for _, flag := range []string{"--wait=bogus", "--timeout=-1s"} {
		if _, err := execute(t, "", "review", "--path", dir, flag); err == nil {
			t.Errorf("accepted invalid option %s on an excluded-only worktree", flag)
		}
	}
}
