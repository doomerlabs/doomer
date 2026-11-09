package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
)

func TestManagedHooksRequireApplicableSkill(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		for _, scope := range []string{"missing", "repository", "personal", "other-agent-personal", "linked-worktree"} {
			t.Run(agent+"/"+scope, func(t *testing.T) {
				isolate(t)
				home := t.TempDir()
				t.Setenv("HOME", home)
				repo := newWatchTestRepo(t)
				if scope == "linked-worktree" {
					linked := filepath.Join(t.TempDir(), "linked")
					if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "hook-scope", linked).CombinedOutput(); err != nil {
						t.Fatal(string(out), err)
					}
					repo = linked
				}
				if scope != "missing" {
					base, dir := repo, ".agents"
					if scope == "personal" || scope == "other-agent-personal" {
						base = home
						if (agent == "claude") != (scope == "other-agent-personal") {
							dir = ".claude"
						}
					}
					path := filepath.Join(base, dir, "skills/doomer-review/SKILL.md")
					text, err := managed.BootstrapText(managed.Policy{Protocol: managed.Protocol, Channel: "stable"})
					if err != nil {
						t.Fatal(err)
					}
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(text), 0600); err != nil {
						t.Fatal(err)
					}
				}
				for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
					raw, _ := json.Marshal(watch.HookInput{CWD: repo, Event: event, Session: "unprepared"})
					output, err := execute(t, string(raw), "agent-hook", "--agent", agent, "--managed-task-adapter")
					if err != nil {
						t.Fatal(output, err)
					}
					if scope == "missing" || scope == "other-agent-personal" || event == "Stop" {
						var feedback map[string]any
						if json.Unmarshal([]byte(output), &feedback) != nil || len(feedback) != 0 {
							t.Fatalf("unconfigured hook must be silent: %s", output)
						}
					} else if !strings.Contains(output, "prepare") {
						t.Fatalf("configured hook lost activation: %s", output)
					}
				}
			})
		}
	}
}

func TestPrepareWithoutSkillExplainsMissingSetup(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	repo := newWatchTestRepo(t)
	out, err := execute(t, "", "prepare", "--agent", "codex", "--repo", repo, "--prompt", "Create a file")
	if err != nil {
		t.Fatal(out, err)
	}
	var result map[string]any
	if json.Unmarshal([]byte(out), &result) != nil || result["state"] != "not_configured" {
		t.Fatal(out)
	}
}

func TestHookDoesNotHideInvalidSkill(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	repo := newWatchTestRepo(t)
	path := filepath.Join(repo, ".agents/skills/doomer-review/SKILL.md")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte("invalid skill"), 0600)
	input, _ := json.Marshal(watch.HookInput{CWD: repo, Event: "PreToolUse", Session: "test"})
	if out, err := execute(t, string(input), "agent-hook", "--agent", "codex"); err == nil {
		t.Fatal("invalid skill silently ignored", out)
	}
}
