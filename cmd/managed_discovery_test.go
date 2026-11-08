package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeamInstallDistributesDiscoveryWithoutLocalActivation(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	original := "# Team rules\n\nKeep this exactly.\n"
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err := execute(t, "", "install", "--repo", repo)
		if err != nil {
			t.Fatal(out, err)
		}
		for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
			if !strings.Contains(out, name) {
				t.Fatalf("install did not report %s: %s", name, out)
			}
			data, err := os.ReadFile(filepath.Join(repo, name))
			if err != nil || strings.Count(string(data), discoveryStart) != 1 {
				t.Fatalf("non-idempotent %s: %s (%v)", name, data, err)
			}
		}
	}
	data, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if !bytes.HasPrefix(data, []byte(original)) || !bytes.Contains(data, []byte(".agents/skills/doomer-review/SKILL.md")) {
		t.Fatalf("lost existing instructions or Codex discovery: %s", data)
	}
	info, _ := os.Stat(filepath.Join(repo, "AGENTS.md"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("changed existing instruction permissions")
	}
	claude, _ := os.ReadFile(filepath.Join(repo, "CLAUDE.md"))
	if !bytes.HasPrefix(claude, []byte("@AGENTS.md\n")) || !bytes.Contains(claude, []byte(".claude/skills/doomer-review/SKILL.md")) {
		t.Fatalf("missing Claude discovery or shared rules: %s", claude)
	}
	// A teammate receives only committed files, not the installer's private state.
	reviewGit(t, repo, "add", ".")
	reviewGit(t, repo, "commit", "-m", "Team Doomer setup")
	clone := filepath.Join(t.TempDir(), "coworker")
	reviewGit(t, repo, "clone", "--quiet", repo, clone)
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", ".agents/skills/doomer-review/SKILL.md", ".claude/skills/doomer-review/SKILL.md"} {
		want, _ := os.ReadFile(filepath.Join(repo, name))
		got, err := os.ReadFile(filepath.Join(clone, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("teammate missing %s: %v", name, err)
		}
	}
	for _, name := range []string{".codex/hooks.json", ".claude/settings.json"} {
		if _, err := os.Stat(filepath.Join(repo, name)); !os.IsNotExist(err) {
			t.Fatalf("team installation configured local hooks: %s", name)
		}
	}
	plan := &resetPlan{}
	plan.discoverInstructions(repo)
	// The existing reset path recognizes and removes the new blocks too.
	found := 0
	for _, entry := range plan.Entries {
		if entry.Kind == "instructions" {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("reset did not discover both instruction blocks: %+v", plan.Entries)
	}
}

func TestTeamInstallRejectsUnsafeInstructionsBeforeWriting(t *testing.T) {
	for _, kind := range []string{"malformed", "duplicate", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			isolate(t)
			repo := newWatchTestRepo(t)
			path := filepath.Join(repo, "CLAUDE.md")
			outside := filepath.Join(t.TempDir(), "outside")
			if kind == "symlink" {
				os.WriteFile(outside, []byte("private"), 0600)
				os.Symlink(outside, path)
			} else {
				text := discoveryStart
				if kind == "duplicate" {
					text += discoveryStart + discoveryEnd
				}
				os.WriteFile(path, []byte(text), 0600)
			}
			if out, err := execute(t, "", "install", "--repo", repo); err == nil {
				t.Fatal("accepted unsafe instructions", out)
			}
			for _, name := range []string{"AGENTS.md", ".agents/skills/doomer-review/SKILL.md"} {
				if _, err := os.Stat(filepath.Join(repo, name)); !os.IsNotExist(err) {
					t.Fatalf("partial installation: %s", name)
				}
			}
		})
	}
}

func TestDiscoveryReplacesOnlyManagedBlock(t *testing.T) {
	repo := t.TempDir()
	original := "before\n" + discoveryStart + "\nold workflow\n" + discoveryEnd + "\nafter\n"
	os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(original), 0644)
	_, after, err := managedDiscoveryInstructions(repo, "AGENTS.md")
	if err != nil || !bytes.HasPrefix(after, []byte("before\n")) || !bytes.HasSuffix(after, []byte("\nafter\n")) || bytes.Contains(after, []byte("old workflow")) {
		t.Fatal(string(after), err)
	}
}

func TestPersonalInstallDoesNotWriteGlobalInstructions(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if out, err := execute(t, "", "install", "--personal"); err != nil {
		t.Fatal(out, err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", ".codex/AGENTS.md", ".claude/CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
			t.Fatalf("personal install wrote global instructions: %s", name)
		}
	}
}

func TestManagedFilesRollbackDifferentContents(t *testing.T) {
	isolate(t)
	repo := t.TempDir()
	first, second := filepath.Join(repo, "AGENTS.md"), filepath.Join(repo, "CLAUDE.md")
	os.WriteFile(first, []byte("original"), 0600)
	os.WriteFile(second, []byte("concurrent change"), 0600)
	err := writeManagedFiles(repo, []string{first, second}, map[string][]byte{first: []byte("new Codex"), second: []byte("new Claude")}, map[string][]byte{first: []byte("original"), second: []byte("old Claude")})
	if err == nil {
		t.Fatal("accepted concurrent edit")
	}
	data, _ := os.ReadFile(first)
	info, _ := os.Stat(first)
	if string(data) != "original" || info.Mode().Perm() != 0600 {
		t.Fatal("rollback lost original content or permissions")
	}
}
