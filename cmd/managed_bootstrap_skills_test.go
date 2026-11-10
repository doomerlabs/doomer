package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/spf13/cobra"
)

func TestSkillsManagedPersonalUpdateIsNotificationOnly(t *testing.T) {
	for _, layout := range []string{"symlink", "copy", "xdg"} {
		t.Run(layout, func(t *testing.T) {
			isolate(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_STATE_HOME", "")
			legacy, err := os.ReadFile("../internal/managed/bootstrap-legacy.md")
			if err != nil {
				t.Fatal(err)
			}
			old := strings.Replace(string(legacy), "{{metadata}}", "  doomer-protocol: \"2\"\n  doomer-update-policy: manual", 1)
			canonical := filepath.Join(home, ".agents/skills/doomer-review/SKILL.md")
			linked := filepath.Join(home, ".claude/skills/doomer-review/SKILL.md")
			if err := os.MkdirAll(filepath.Dir(canonical), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(canonical, []byte(old), 0600); err != nil {
				t.Fatal(err)
			}
			if layout == "symlink" {
				if err := os.MkdirAll(filepath.Dir(filepath.Dir(linked)), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("../../.agents/skills/doomer-review", filepath.Dir(linked)); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(linked), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(linked, []byte(old), 0600); err != nil {
					t.Fatal(err)
				}
			}
			lockPath := filepath.Join(home, ".agents/.skill-lock.json")
			if layout == "xdg" {
				stateHome := t.TempDir()
				t.Setenv("XDG_STATE_HOME", stateHome)
				lockPath = filepath.Join(stateHome, "skills/.skill-lock.json")
				if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
					t.Fatal(err)
				}
			}
			lock := []byte(`{"version":3,"skills":{"doomer-review":{"source":"doomerlabs/skills","sourceType":"github","skillFolderHash":"preserve"}}}`)
			if err := os.WriteFile(lockPath, lock, 0600); err != nil {
				t.Fatal(err)
			}
			for _, agent := range []string{"codex", "claude"} {
				bootstrap, err := managed.ResolveBootstrap(t.TempDir(), home, agent)
				if err != nil || bootstrap.Scope != "personal" {
					t.Fatal(bootstrap, err)
				}
				updates := managedBootstrapUpdates(managed.Task{Bootstrap: bootstrap, Release: "dev"})
				if len(updates) != 2 {
					t.Fatal(updates)
				}
				for _, update := range updates {
					if update.State != "update_available" || update.UpdateManager != "skills" || len(update.UpgradeCommand) != 0 || !reflect.DeepEqual(update.ManualUpdateCommand, []string{"npx", "skills", "update", "doomer-review", "--global"}) {
						t.Fatalf("wrong update route: %+v", update)
					}
				}
				result := managedResult{State: "ready", Instructions: "continue reviewing", BootstrapUpdates: updates}
				prepareAttention(&result)
				if result.State != "ready" || result.AttentionRequired || result.Instructions != "continue reviewing" || !strings.Contains(result.Action, "npx skills update doomer-review --global") || !strings.Contains(result.Action, "Display this notice verbatim") || !strings.Contains(result.Action, "Do not execute") {
					t.Fatalf("notification became an approval gate: %+v", result)
				}
				command := &cobra.Command{Use: "finish"}
				command.SetContext(context.Background())
				var output bytes.Buffer
				command.SetOut(&output)
				if err := outputManaged(command, managed.Store{}, managed.Task{Bootstrap: bootstrap}, managedResult{State: "complete"}); err != nil {
					t.Fatal(err)
				}
				var finished managedResult
				if err := json.Unmarshal(output.Bytes(), &finished); err != nil {
					t.Fatal(err)
				}
				if finished.State != "complete" || finished.AttentionRequired || !strings.Contains(finished.Action, "npx skills update doomer-review --global") {
					t.Fatalf("completion lost the manual update notice: %+v", finished)
				}
			}
			for _, args := range [][]string{
				{"bootstrap", "upgrade", "--personal"},
				{"bootstrap", "upgrade", "--personal", "--accept"},
			} {
				out, err := execute(t, "", args...)
				var result struct {
					State              string `json:"state"`
					AcceptanceRequired bool   `json:"acceptance_required"`
				}
				if err != nil || json.Unmarshal([]byte(out), &result) != nil || result.AcceptanceRequired {
					t.Fatal(out, err)
				}
				if args[len(args)-1] == "--accept" && result.State != "external_update_required" {
					t.Fatal(out)
				}
			}
			for _, path := range []string{canonical, linked, lockPath} {
				want := []byte(old)
				if path == lockPath {
					want = lock
				}
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatal("installer-owned file changed", path, err)
				}
			}
			if layout == "symlink" {
				if target, err := os.Readlink(filepath.Dir(linked)); err != nil || target != "../../.agents/skills/doomer-review" {
					t.Fatal("installer link changed", target, err)
				}
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(canonical), "agents/openai.yaml")); !os.IsNotExist(err) {
				t.Fatal("updater created invocation metadata", err)
			}
		})
	}
}

func TestSkillsInstallerOwnershipRequiresValidMatchingLock(t *testing.T) {
	for _, tc := range []struct {
		name, lock  string
		wantManager bool
		wantError   bool
	}{
		{name: "absent"},
		{name: "other-skill", lock: `{"version":3,"skills":{"other":{"source":"owner/repo","sourceType":"github"}}}`},
		{name: "matching", lock: `{"version":3,"skills":{"doomer-review":{"source":"owner/repo","sourceType":"github"}}}`, wantManager: true},
		{name: "invalid-json", lock: `{`, wantError: true},
		{name: "invalid-shape", lock: `{}`, wantError: true},
		{name: "invalid-entry", lock: `{"version":3,"skills":{"doomer-review":{}}}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", "")
			base := t.TempDir()
			path := filepath.Join(base, ".agents/skills/doomer-review/SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			text, _ := managed.BootstrapText(managed.Policy{Protocol: managed.Protocol, Channel: "stable"})
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.lock != "" {
				if err := os.WriteFile(filepath.Join(base, ".agents/.skill-lock.json"), []byte(tc.lock), 0600); err != nil {
					t.Fatal(err)
				}
			}
			update, err := inspectManagedBootstrap(path, "personal", "dev", base)
			if (err != nil) != tc.wantError || (update.UpdateManager == "skills") != tc.wantManager {
				t.Fatal(update, err)
			}
			project, err := inspectManagedBootstrap(path, "project", "dev", base)
			if err != nil || project.UpdateManager != "" {
				t.Fatal("personal lock changed repository handling", project, err)
			}
		})
	}
}
