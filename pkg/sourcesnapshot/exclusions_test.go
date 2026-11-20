package sourcesnapshot

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewExclusionsApplyToBothTrees(t *testing.T) {
	dir, _ := sourceRepo(t)
	t.Setenv(ReviewFileLimitEnv, "256")
	sourceFile(t, dir, ".gitignore", "ignored/\ntracked.txt\n")
	sourceFile(t, dir, "tracked.txt", "tracked before\n")
	sourceFile(t, dir, "old-binary", "\x00old binary")
	sourceFile(t, dir, "new-binary", "old text\n")
	sourceFile(t, dir, "old-large", strings.Repeat("a", 257))
	sourceFile(t, dir, "new-large", "old small text\n")
	sourceFile(t, dir, "Generated/obsolete.txt", "old generated source\n")
	git(t, dir, "add", ".")
	git(t, dir, "add", "-f", "tracked.txt")
	git(t, dir, "commit", "-m", "base with exclusions")
	base := git(t, dir, "rev-parse", "HEAD")

	sourceFile(t, dir, ".doomerignore", "/Generated/*\n!/Generated/README.md\n**/*.xcuserstate\n/ignored/*\n!/ignored/keep.txt\n/cache/\n!/cache/keep\n")
	sourceFile(t, dir, "old-binary", "new text\n")
	sourceFile(t, dir, "new-binary", "\xffnew binary")
	sourceFile(t, dir, "old-large", "new small text\n")
	sourceFile(t, dir, "new-large", strings.Repeat("b", 257))
	sourceFile(t, dir, "tracked.txt", "tracked after\n")
	sourceFile(t, dir, "Generated/README.md", "keep\n")
	sourceFile(t, dir, "nested/Generated/readme.txt", "keep nested\n")
	sourceFile(t, dir, "nested/project.xcuserstate", "ignore\n")
	sourceFile(t, dir, "ignored/keep.txt", "Git ignores me\n")
	sourceFile(t, dir, "cache/keep", "parent ignored\n")
	if err := os.Remove(filepath.Join(dir, "Generated/obsolete.txt")); err != nil {
		t.Fatal(err)
	}

	got, err := CaptureWorktree(context.Background(), CaptureOptions{Workspace: true, Path: dir, Base: base, ShowSkipped: true})
	if err != nil {
		t.Fatal(err)
	}
	wantSkipped := map[string]string{
		"old-binary": SkipBinary, "new-binary": SkipBinary,
		"old-large": SkipSize, "new-large": SkipSize,
		"Generated/obsolete.txt": SkipDoomerIgnore, "nested/project.xcuserstate": SkipDoomerIgnore,
		"cache/keep": SkipDoomerIgnore, "ignored/": SkipGitIgnore,
	}
	if !maps.Equal(got.Skipped, wantSkipped) {
		t.Fatalf("skipped: %v, want %v", got.Skipped, wantSkipped)
	}
	manifest, err := DecodeWorkspace(got.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	warmup, _, err := CaptureBaseline(context.Background(), CaptureOptions{Path: dir, Base: base})
	if err != nil {
		t.Fatal(err)
	}
	for _, tree := range [][]Entry{manifest.Base, manifest.Target, warmup.Base} {
		for _, entry := range tree {
			if _, excluded := wantSkipped[entry.Path]; excluded {
				t.Fatalf("excluded entry in snapshot: %s", entry.Path)
			}
		}
	}
	target := map[string]bool{}
	for _, entry := range manifest.Target {
		target[entry.Path] = true
	}
	for _, name := range []string{"Generated/README.md", "nested/Generated/readme.txt", "tracked.txt"} {
		if !target[name] {
			t.Errorf("eligible path omitted: %s", name)
		}
	}
	if target["ignored/keep.txt"] {
		t.Error(".doomerignore negation overrode Git ignore rules")
	}
}

func TestReviewWithOnlyExcludedChanges(t *testing.T) {
	dir, base := sourceRepo(t)
	t.Setenv(ReviewFileLimitEnv, "")
	sourceFile(t, dir, "asset.png", "\x00image")
	sourceFile(t, dir, "large.txt", strings.Repeat("x", DefaultReviewFileBytes+1))
	got, err := CaptureWorktree(context.Background(), CaptureOptions{Workspace: true, Path: dir, Base: base})
	if !errors.Is(err, ErrNoChanges) || got.Skipped["asset.png"] != SkipBinary || got.Skipped["large.txt"] != SkipSize {
		t.Fatalf("expected no review and both exclusions, got %v, %v", err, got.Skipped)
	}
}
