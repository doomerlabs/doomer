package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
)

func TestBootstrapUpgradeRequiresAcceptanceAndPreservesPolicy(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	legacy, err := os.ReadFile("../internal/managed/bootstrap-legacy.md")
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(string(legacy), "{{metadata}}", "  doomer-protocol: \"2\"\n  doomer-update-policy: pinned\n  doomer-version: 2026.11.6.9", 1)
	paths := []string{filepath.Join(repo, ".agents/skills/doomer-review/SKILL.md"), filepath.Join(repo, ".claude/skills/doomer-review/SKILL.md")}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(old), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, error) {
		command := newManagedBootstrapCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetArgs(append(args, "--repo", repo))
		err := command.Execute()
		return output.String(), err
	}
	output, err := run("upgrade")
	if err != nil || !strings.Contains(output, `"acceptance_required":true`) || !strings.Contains(output, `"update_available"`) {
		t.Fatal(output, err)
	}
	for _, path := range paths {
		data, _ := os.ReadFile(path)
		if string(data) != old {
			t.Fatal("changed without acceptance")
		}
	}
	// A valid old copy must not turn a sibling's local modifications into
	// an actionable upgrade that would overwrite those edits.
	if err := os.WriteFile(paths[1], []byte(old+"\nCustom instructions.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mixed, err := run("upgrade")
	if err != nil || !strings.Contains(mixed, `"acceptance_required":false`) {
		t.Fatal("mixed modified scope requested acceptance", mixed, err)
	}
	if err := os.WriteFile(paths[1], []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	offered := managedBootstrapUpdates(managed.Task{Repo: repo, Bootstrap: managed.Bootstrap{Path: paths[0], Scope: "project"}})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if len(offered) != 2 || offered[0].DeferCommand[0] != executable || offered[0].UpgradeCommand[0] != executable {
		t.Fatalf("maintenance was routed through an old PATH launcher: %+v", offered)
	}
	output, err = run("defer")
	if err != nil || !strings.Contains(output, "bootstrap_deferred") {
		t.Fatal(output, err)
	}
	updates := managedBootstrapUpdates(managed.Task{Repo: repo, Bootstrap: managed.Bootstrap{Path: paths[0], Scope: "project"}})
	if len(updates) != 0 {
		t.Fatalf("deferred offer repeated: %+v", updates)
	}
	output, err = run("status")
	if err != nil || !strings.Contains(output, "update_available") {
		t.Fatal("manual inspection lost the update", output, err)
	}
	output, err = run("upgrade", "--accept")
	if err != nil || !strings.Contains(output, "bootstrap_upgraded") {
		t.Fatal(output, err)
	}
	for _, path := range paths {
		b, err := managed.ReadBootstrap(path, "project")
		if err != nil || b.Revision != managed.BootstrapVersion || b.Policy.Channel != "pinned" || b.Policy.Version != "2026.11.6.9" {
			t.Fatal(b, err)
		}
	}
	if _, err = run("upgrade", "--accept"); err != nil {
		t.Fatal("retry failed", err)
	}
	output, err = run("upgrade")
	if err != nil || !strings.Contains(output, `"acceptance_required":false`) {
		t.Fatal("current skill requested acceptance", output, err)
	}

	data, _ := os.ReadFile(paths[1])
	if err = os.WriteFile(paths[1], append(data, []byte("\nCustom instructions.\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = run("upgrade", "--accept"); err == nil {
		t.Fatal("overwrote custom wrapper")
	}
	output, err = run("upgrade")
	if err != nil || !strings.Contains(output, `"acceptance_required":false`) {
		t.Fatal("modified skill requested an upgrade", output, err)
	}

}

func TestBootstrapUpgradeDetectsRacesAndRollsBack(t *testing.T) {
	isolate(t)
	base := t.TempDir()
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	old, current := []byte("old"), []byte("new")
	os.WriteFile(first, old, 0600)
	os.WriteFile(second, []byte("concurrent edit"), 0600)
	err := writeBootstrapFiles(base, []string{first, second}, current, map[string][]byte{first: old, second: old})
	if err == nil {
		t.Fatal("accepted concurrent change")
	}
	data, _ := os.ReadFile(first)
	if !bytes.Equal(data, old) {
		t.Fatal("first file was not restored")
	}
	data, _ = os.ReadFile(second)
	if string(data) != "concurrent edit" {
		t.Fatal("concurrent edit overwritten")
	}
}

func TestPersonalBootstrapUpgradeAddsDiscoveryAndPreservesPolicy(t *testing.T) {
	for _, customConfig := range []bool{false, true} {
		t.Run(fmt.Sprint("custom-config-", customConfig), func(t *testing.T) {
			isolate(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			policy := managed.Policy{Protocol: managed.Protocol, Channel: "manual"}
			old, err := managed.BootstrapText(policy)
			if err != nil {
				t.Fatal(err)
			}
			// A previous personal install may have only one agent's copy.
			path := filepath.Join(home, ".agents/skills/doomer-review/SKILL.md")
			if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, []byte(old), 0600); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(filepath.Dir(path), "agents/openai.yaml")
			wantConfig := personalCodexSkillConfig
			if customConfig {
				wantConfig = "interface:\n  display_name: Custom\npolicy:\n  allow_implicit_invocation: false\n"
				if err = os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(configPath, []byte(wantConfig), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, err := execute(t, "", "bootstrap", "upgrade", "--personal")
			if err != nil || !strings.Contains(out, `"acceptance_required":true`) {
				t.Fatal(out, err)
			}
			before, err := os.ReadFile(path)
			if err != nil || string(before) != old {
				t.Fatal("upgrade changed skill without acceptance", err)
			}
			for i := 0; i < 2; i++ {
				out, err = execute(t, "", "bootstrap", "upgrade", "--personal", "--accept")
				if err != nil {
					t.Fatal(out, err)
				}
			}
			want, _ := managed.BootstrapTextForScope(policy, "personal")
			for _, dir := range []string{".agents", ".claude"} {
				path := filepath.Join(home, dir, "skills/doomer-review/SKILL.md")
				data, err := os.ReadFile(path)
				if err != nil || string(data) != want {
					t.Fatal("upgrade lost policy or personal trigger", dir, err)
				}
				update, err := managed.InspectBootstrap(path, "personal", "dev")
				if err != nil || update.State != "current" {
					t.Fatal(update, err)
				}
			}
			config, err := os.ReadFile(configPath)
			if err != nil || string(config) != wantConfig {
				t.Fatal("upgrade lost invocation metadata", string(config), err)
			}
		})
	}
}

func TestPersonalBootstrapRejectsUnsafeConfigWithoutPartialWrites(t *testing.T) {
	for _, operation := range []string{"install", "upgrade"} {
		t.Run(operation, func(t *testing.T) {
			isolate(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := filepath.Join(home, ".agents/skills/doomer-review/SKILL.md")
			configPath := filepath.Join(filepath.Dir(path), "agents/openai.yaml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(outside, []byte("custom"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, configPath); err != nil {
				t.Fatal(err)
			}
			args := []string{"install", "--personal"}
			old := ""
			if operation == "upgrade" {
				old, _ = managed.BootstrapText(managed.Policy{Protocol: managed.Protocol, Channel: "stable"})
				if err := os.WriteFile(path, []byte(old), 0600); err != nil {
					t.Fatal(err)
				}
				args = []string{"bootstrap", "upgrade", "--personal", "--accept"}
			}
			if out, err := execute(t, "", args...); err == nil {
				t.Fatal("accepted symlinked metadata", out)
			}
			data, err := os.ReadFile(path)
			if operation == "install" && !os.IsNotExist(err) || operation == "upgrade" && (err != nil || string(data) != old) {
				t.Fatal("partially wrote the skill", string(data), err)
			}
			if _, err := os.Stat(filepath.Join(home, ".claude/skills/doomer-review/SKILL.md")); !os.IsNotExist(err) {
				t.Fatal("partially created sibling skill", err)
			}
			data, err = os.ReadFile(outside)
			if err != nil || string(data) != "custom" {
				t.Fatal("changed symlink target", err)
			}
		})
	}
}

func TestPersonalBootstrapConfigRaceRollsBackSkills(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprint("deleted-", deleted), func(t *testing.T) {
			isolate(t)
			home := t.TempDir()
			policy := managed.Policy{Protocol: managed.Protocol, Channel: "manual"}
			configPath := filepath.Join(home, ".agents/skills/doomer-review/agents/openai.yaml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, []byte(personalCodexSkillConfig), 0600); err != nil {
				t.Fatal(err)
			}
			paths, contents, expected, err := managedBootstrapFiles(home, policy, "personal")
			if err != nil {
				t.Fatal(err)
			}
			if deleted {
				err = os.Remove(configPath)
			} else {
				err = os.WriteFile(configPath, []byte("concurrent edit"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := writeManagedFiles(home, paths, contents, expected); err == nil {
				t.Fatal("accepted concurrent config change")
			}
			for _, path := range paths[:2] {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("failed to roll back skill", path, err)
				}
			}
			data, err := os.ReadFile(configPath)
			if deleted && !os.IsNotExist(err) || !deleted && (err != nil || string(data) != "concurrent edit") {
				t.Fatal("overwrote concurrent config change", string(data), err)
			}
		})
	}
}

func TestBootstrapInspectionFailureIsAdvisory(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".agents/skills/doomer-review/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("malformed wrapper"), 0600); err != nil {
		t.Fatal(err)
	}
	updates := managedBootstrapUpdates(managed.Task{Bootstrap: managed.Bootstrap{Path: path, Scope: "project"}})
	if len(updates) != 1 || updates[0].State != "inspection_failed" {
		t.Fatal(updates)
	}
}

func TestBootstrapDecisionCommandsRemainVisibleAndScoped(t *testing.T) {
	for _, scope := range []string{"project", "personal"} {
		t.Run(scope, func(t *testing.T) {
			isolate(t)
			base := filepath.Join(t.TempDir(), "scope with spaces")
			path := filepath.Join(base, ".agents/skills/doomer-review/SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if scope == "project" {
				reviewGit(t, base, "init")
			}
			legacy, err := os.ReadFile("../internal/managed/bootstrap-legacy.md")
			if err != nil {
				t.Fatal(err)
			}
			old := strings.Replace(string(legacy), "{{metadata}}", "  doomer-protocol: \"2\"\n  doomer-update-policy: stable", 1)
			if err := os.WriteFile(path, []byte(old), 0600); err != nil {
				t.Fatal(err)
			}
			task := managed.Task{Repo: base, Bootstrap: managed.Bootstrap{Path: path, Scope: scope}}
			updates := managedBootstrapUpdates(task)
			if len(updates) != 1 {
				t.Fatalf("expected one actionable update: %+v", updates)
			}
			update := updates[0]
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			target := []string{"--repo", base}
			if scope == "personal" {
				target = []string{"--personal"}
			}
			if !reflect.DeepEqual(update.DeferCommand, append([]string{executable, "bootstrap", "defer"}, target...)) ||
				!reflect.DeepEqual(update.UpgradeCommand, append([]string{executable, "bootstrap", "upgrade", "--accept"}, target...)) {
				t.Fatalf("commands lost runtime or scope: %+v", update)
			}
			encoded, err := json.Marshal(update)
			if err != nil {
				t.Fatal(err)
			}
			preview := string(encoded[:min(len(encoded), 2048)])
			for _, required := range []string{"defer_command", "upgrade_command", "Maybe later", "bootstrap_deferred", "only after acceptance"} {
				if !strings.Contains(preview, required) {
					t.Fatalf("decision missing from short preview: %s", required)
				}
			}
			if strings.Contains(update.Action, "## Skill upgrade decisions") {
				t.Fatal("duplicated full runtime rules obscure the decision")
			}
			if scope == "project" {
				command := newManagedBootstrapCommand()
				command.SetOut(new(bytes.Buffer))
				command.SetArgs(update.DeferCommand[2:])
				if err := command.Execute(); err != nil {
					t.Fatal(err)
				}
				if got := managedBootstrapUpdates(task); len(got) != 0 {
					t.Fatalf("executing returned command did not suppress offer: %+v", got)
				}
				actual, err := os.ReadFile(path)
				if err != nil || string(actual) != old {
					t.Fatal("deferral changed the skill", err)
				}
			}
		})
	}
}
