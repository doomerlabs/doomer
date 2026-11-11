package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
)

// Run real OS-denied writes, including a valid 0700 directory where skipping
// chmod merely moves the failure to the first state write.
func TestSandboxedPreparationStateAccess(t *testing.T) {
	if root := os.Getenv("DOOMER_STATE_ACCESS_TEST_ROOT"); root != "" {
		os.Setenv("DOOMER_CONFIG_DIR", root)
		if os.Getenv("DOOMER_STATE_ACCESS_TEST_INIT") == "1" {
			if err := (managed.Store{Root: filepath.Join(root, "managed")}).Init(); err != nil {
				t.Fatal(err)
			}
			return
		}
		out, err := execute(t, "", "prepare", "--agent", "codex", "--repo", os.Getenv("DOOMER_STATE_ACCESS_TEST_REPO"), "--session", "sandbox-session", "--prompt", "Make the requested edit")
		if err == nil {
			t.Fatal("sandboxed preparation unexpectedly succeeded")
		}
		fmt.Print(out)
		return
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("macOS sandbox-exec unavailable")
	}
	repo := newWatchTestRepo(t)
	bootstrap, err := managed.BootstrapText(managed.Policy{Protocol: managed.Protocol, Channel: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	skill := filepath.Join(repo, ".agents/skills/doomer-review/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skill), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, []byte(bootstrap), 0600); err != nil {
		t.Fatal(err)
	}
	// Symlinks such as /var -> /private/var must resolve before building the policy.
	for _, tc := range []struct {
		name        string
		mode        os.FileMode
		bindingOnly bool
	}{
		{"private-state", 0700, false}, {"chmod", 0755, false}, {"saved-task", 0700, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := tc.mode
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(root, "managed")
			if err := os.Mkdir(state, mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(state, mode); err != nil {
				t.Fatal(err)
			}
			denied := root
			if tc.bindingOnly {
				denied = filepath.Join(state, "sessions")
			}
			policy := fmt.Sprintf("(version 1)(allow default)(deny file-write* (subpath %q))", denied)
			run := func(init bool) []byte {
				command := exec.Command("sandbox-exec", "-p", policy, os.Args[0], "-test.run=^TestSandboxedPreparationStateAccess$")
				command.Env = append(os.Environ(), "DOOMER_STATE_ACCESS_TEST_ROOT="+root, "DOOMER_STATE_ACCESS_TEST_REPO="+repo)
				if init {
					command.Env = append(command.Env, "DOOMER_STATE_ACCESS_TEST_INIT=1")
				}
				out, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("sandbox helper: %v\n%s", err, out)
				}
				return out
			}
			if mode == 0700 {
				run(true)
			} // Correct permissions do not need a write.
			out := run(false)
			var result managedStateAccess
			line := strings.SplitN(string(out), "\n", 2)[0]
			if err := json.Unmarshal([]byte(line), &result); err != nil {
				t.Fatal(err, string(out))
			}
			if result.State != "state_access_required" || len(result.RetryCommand) == 0 {
				t.Fatal(result)
			}
			if tc.bindingOnly {
				tasks, err := filepath.Glob(filepath.Join(state, "tasks", "*.json"))
				if err != nil || len(tasks) != 1 || result.Task == "" || !strings.Contains(result.RetryCommandDisplay, "'--task' '"+result.Task+"'") {
					t.Fatal("did not resume saved task", result, tasks, err)
				}
				if strings.Contains(result.RetryCommandDisplay, "--base") {
					t.Fatal("changed saved baseline", result)
				}
				return
			}
			if result.Task != "" || !strings.Contains(result.RetryCommandDisplay, "--base=") {
				t.Fatal("unsaved task lost baseline", result)
			}
			if mode == 0700 && result.Operation == "chmod" {
				t.Fatal("unnecessary chmod", result)
			}
			if mode == 0755 && result.Operation != "chmod" {
				t.Fatal("expected chmod denial", result)
			}
			entries, err := os.ReadDir(state)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed preparation wrote state", entries, err)
			}
		})
	}
}
