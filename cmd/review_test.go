package cmd

import (
	"os"
	"os/exec"
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
