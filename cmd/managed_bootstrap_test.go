package cmd

import (
	"bytes"
	"os"
	"path/filepath"
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
	data, _ := os.ReadFile(paths[1])
	if err = os.WriteFile(paths[1], append(data, []byte("\nCustom instructions.\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = run("upgrade", "--accept"); err == nil {
		t.Fatal("overwrote custom wrapper")
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
