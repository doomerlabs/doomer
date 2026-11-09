package sourcesnapshot

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGitIdentityUsesEffectiveConfigAndDoesNotChangeSnapshot(t *testing.T) {
	dir, _ := sourceRepo(t)
	sourceFile(t, dir, "code.go", "changed\n")
	git(t, dir, "config", "--global", "user.name", "Global User")
	git(t, dir, "config", "--global", "user.email", "global@example.test")
	first := capture(t, dir, "HEAD")
	if first.GitIdentity == nil || *first.GitIdentity != (GitIdentity{Name: "Global User", Email: "global@example.test", Source: "git_config"}) {
		t.Fatalf("global identity: %+v", first.GitIdentity)
	}
	git(t, dir, "config", "user.name", "Repository User")
	git(t, dir, "config", "user.email", "repo@example.test")
	second := capture(t, dir, "HEAD")
	if second.GitIdentity == nil || *second.GitIdentity != (GitIdentity{Name: "Repository User", Email: "repo@example.test", Source: "git_config"}) {
		t.Fatalf("local identity: %+v", second.GitIdentity)
	}
	if string(first.Bytes) != string(second.Bytes) || first.Digest != second.Digest {
		t.Fatal("identity changed source snapshot")
	}
	for _, value := range []string{"Repository User", "repo@example.test", "Global User", "global@example.test"} {
		if strings.Contains(string(second.Bytes), value) {
			t.Fatal("identity leaked into snapshot")
		}
	}
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, dir, "worktree", "add", "-b", "linked", linked)
	sourceFile(t, linked, "code.go", "linked change\n")
	// Conditional includes must be evaluated in the linked checkout's context.
	config := filepath.Join(t.TempDir(), "identity")
	sourceFile(t, filepath.Dir(config), filepath.Base(config), "[user]\n email = linked@example.test\n")
	git(t, dir, "config", "--add", "includeIf.onbranch:linked.path", config)
	third := capture(t, linked, "HEAD")
	if third.GitIdentity == nil || third.GitIdentity.Email != "linked@example.test" {
		t.Fatalf("linked conditional identity: %+v", third.GitIdentity)
	}
}

func TestGitIdentityMissingAndInvalidFieldsAreOptional(t *testing.T) {
	dir, _ := sourceRepo(t)
	sourceFile(t, dir, "code.go", "changed\n")
	t.Setenv("GIT_AUTHOR_EMAIL", "author-env@example.test")
	t.Setenv("GIT_COMMITTER_EMAIL", "committer-env@example.test")
	if got := capture(t, dir, "HEAD").GitIdentity; got != nil {
		t.Fatalf("inferred identity without config: %+v", got)
	}
	git(t, dir, "config", "user.name", "Valid Name")
	for _, invalid := range []string{"", "bad\nemail", strings.Repeat("x", 321)} {
		git(t, dir, "config", "user.email", invalid)
		got := capture(t, dir, "HEAD").GitIdentity
		if got == nil || got.Name != "Valid Name" || got.Email != "" {
			t.Fatalf("invalid email retained: %+v", got)
		}
	}
	git(t, dir, "config", "user.name", strings.Repeat("x", 257))
	if got := capture(t, dir, "HEAD").GitIdentity; got != nil {
		t.Fatalf("invalid identity retained: %+v", got)
	}
}
