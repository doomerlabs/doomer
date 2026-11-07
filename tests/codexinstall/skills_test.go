package main

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateSkillsGuide(t *testing.T) {
	root := t.TempDir()
	c := config{SkillsInstaller: "# fixture installer\n", SkillsGuide: "Read https://raw.githubusercontent.com/doomerlabs/skills/pinned/scripts/install.py with SHA `" + strings.Repeat("a", 64) + "`."}
	location, err := writeCandidateSkills(c, root)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(parsed.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "file:") || !strings.Contains(string(raw), fmt.Sprintf("%x", sha256.Sum256([]byte(c.SkillsInstaller)))) {
		t.Fatalf("candidate guide does not bind its installer: %s", raw)
	}
	if _, err := writeCandidateSkills(config{SkillsGuide: "ambiguous guide"}, root); err == nil {
		t.Fatal("ambiguous installer accepted")
	}
}

func TestCandidateSkillsPreservesInstallationScope(t *testing.T) {
	for _, scenario := range []string{"install", "personal"} {
		t.Run(scenario, func(t *testing.T) {
			home, repo := t.TempDir(), t.TempDir()
			root := repo
			if scenario == "personal" {
				root = home
			}
			c := config{Scenario: scenario, SkillsBootstrap: "candidate wrapper"}
			if err := installCandidateSkills(c, home, repo); err == nil {
				t.Fatal("accepted missing real installation")
			}
			for _, path := range []string{".agents/skills/doomer-review/SKILL.md", ".claude/skills/doomer-review/SKILL.md"} {
				destination := filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(destination, []byte("published wrapper"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := installCandidateSkills(c, home, repo); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(root, ".agents/skills/doomer-review/SKILL.md"))
			if err != nil || string(raw) != c.SkillsBootstrap {
				t.Fatalf("wrapper mismatch: %s, %v", raw, err)
			}
		})
	}
}
