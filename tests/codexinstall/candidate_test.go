package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
)

func TestCandidateAssemblyAcceptsUnrecognizedPublishedBootstrap(t *testing.T) {
	for _, candidate := range []struct{ version, state string }{
		{"2026.11.8.6", "incompatible"},
		{managed.BootstrapVersion, "modified"},
	} {
		t.Run(candidate.version, func(t *testing.T) {
			testCandidateAssemblyAcceptsUnrecognizedBootstrap(t, candidate.version, candidate.state)
		})
	}
}

func testCandidateAssemblyAcceptsUnrecognizedBootstrap(t *testing.T, candidateVersion, expectedState string) {
	for _, scenario := range []string{"install", "personal"} {
		for _, source := range []string{"generated", "skills", "repository"} {
			t.Run(scenario+"/"+source, func(t *testing.T) {
				home, repo := t.TempDir(), t.TempDir()
				root := repo
				if scenario == "personal" {
					root = home
				}
				policy := managed.Policy{Protocol: managed.Protocol, Channel: "manual"}
				generated, err := managed.BootstrapText(policy)
				if err != nil {
					t.Fatal(err)
				}
				// A valid published wrapper whose contents differ from the runner's
				// template reproduces the production upgrade command's refusal.
				published := generated + "\nPublished guide changed independently.\n"
				for _, agent := range []string{".agents", ".claude"} {
					path := filepath.Join(root, agent, "skills/doomer-review/SKILL.md")
					writeCandidateTestFile(t, path, []byte(published))
					update, err := managed.InspectBootstrap(path, "project", candidateVersion)
					if err != nil || update.State != expectedState {
						t.Fatalf("fixture must reproduce rejection: %+v, %v", update, err)
					}
				}
				c := config{Scenario: scenario, CandidateVersion: candidateVersion, CandidateFile: filepath.Join(t.TempDir(), "candidate")}
				// This is deliberately not executable code: fixture assembly must inject
				// the candidate, not invoke its production bootstrap upgrade command.
				writeCandidateTestFile(t, c.CandidateFile, []byte("candidate runtime"))
				runtime := runtimeversion.Binary(home, c.CandidateVersion)
				writeCandidateTestFile(t, runtime, []byte("published runtime"))
				writeCandidateTestFile(t, filepath.Join(home, ".local/bin/doomer"), []byte("published shim"))
				record, err := json.Marshal(selfupdate.Record{Version: c.CandidateVersion})
				if err != nil {
					t.Fatal(err)
				}
				writeCandidateTestFile(t, filepath.Join(filepath.Dir(runtime), "installation.json"), record)
				if source == "skills" {
					c.SkillsBootstrap = "candidate skills with fixture guide"
				}
				if source == "repository" {
					c.RepoBootstraps = map[string]string{".agents": "codex candidate", ".claude": "claude candidate"}
				}
				if err := assembleCandidateInstallation(c, home, repo); err != nil {
					t.Fatal(err)
				}
				for _, agent := range []string{".agents", ".claude"} {
					want := generated
					if personalScenario(scenario) {
						want, err = managed.BootstrapTextForScope(policy, "personal")
						if err != nil {
							t.Fatal(err)
						}
					}
					if source == "skills" {
						want = c.SkillsBootstrap
					}
					if source == "repository" {
						want = c.RepoBootstraps[agent]
					}
					got, err := os.ReadFile(filepath.Join(root, agent, "skills/doomer-review/SKILL.md"))
					if err != nil || string(got) != want {
						t.Fatalf("%s wrapper mismatch: %q, %v", agent, got, err)
					}
				}
				for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
					data, err := os.ReadFile(filepath.Join(repo, name))
					if scenario == "personal" {
						if !os.IsNotExist(err) {
							t.Fatalf("personal assembly wrote %s", name)
						}
					} else if err != nil || !strings.Contains(string(data), "Before editing repository files") {
						t.Fatalf("candidate discovery missing from %s: %v", name, err)
					}
				}
				for _, path := range []string{runtime, filepath.Join(home, ".local/bin/doomer")} {
					got, err := os.ReadFile(path)
					if err != nil || string(got) != "candidate runtime" {
						t.Fatalf("runtime not injected: %q, %v", got, err)
					}
				}
			})
		}
	}
}

func TestCandidateAssemblyRequiresPublicInstallation(t *testing.T) {
	c := config{CandidateFile: filepath.Join(t.TempDir(), "candidate"), CandidateVersion: "2026.11.8.5", SkillsBootstrap: "candidate"}
	writeCandidateTestFile(t, c.CandidateFile, []byte("candidate runtime"))
	err := assembleCandidateInstallation(c, t.TempDir(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "public installation did not produce") {
		t.Fatal(err)
	}
}

func TestCandidateAssemblyRequiresBothPublicBootstraps(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".agents/skills/doomer-review/SKILL.md")
	writeCandidateTestFile(t, path, []byte("published"))
	err := assembleCandidateInstallation(config{SkillsBootstrap: "candidate"}, t.TempDir(), repo)
	if err == nil || !strings.Contains(err.Error(), "candidate bootstrap was not installed") {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "published" {
		t.Fatalf("incomplete install modified: %q, %v", got, err)
	}
}

func writeCandidateTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCandidateDiscoveryPreservesExistingInstructions(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "AGENTS.md")
	writeCandidateTestFile(t, path, []byte("Keep the team's rules.\n"))
	for i := 0; i < 2; i++ {
		if err := installCandidateDiscovery(repo); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(data), "Keep the team's rules.\n") || strings.Count(string(data), "<!-- doomer-review:start -->") != 1 {
		t.Fatalf("lost or duplicated instructions: %q, %v", data, err)
	}
}
