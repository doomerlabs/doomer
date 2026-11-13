package cmd

import (
	"context"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestUpgradeDelegatesHomebrewWithoutReplacingItsBinary(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "Cellar", "doomer", "2026.11.1.1", "bin", "doomer")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("brew-owned"), 0755); err != nil {
		t.Fatal(err)
	}
	oldExecutable, oldRun := upgradeExecutable, upgradeRun
	t.Cleanup(func() { upgradeExecutable = oldExecutable; upgradeRun = oldRun })
	upgradeExecutable = func() (string, error) { return binary, nil }
	for _, check := range []bool{true, false} {
		var got []string
		upgradeRun = func(_ context.Context, _ *cobra.Command, args []string) error { got = args; return nil }
		c := newUpgradeCommand()
		if check {
			c.SetArgs([]string{"--check"})
		}
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
		want := []string{"brew", "upgrade", "doomer"}
		if check {
			want = []string{"brew", "outdated", "doomer"}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("command %v, want %v", got, want)
		}
	}
	raw, _ := os.ReadFile(binary)
	if string(raw) != "brew-owned" {
		t.Fatal("replaced package-manager-owned binary")
	}
}

func TestDirectUpgradeRejectsConcurrentReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "doomer")
	if err := os.WriteFile(path, []byte("before"), 0755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	other := filepath.Join(root, "other")
	_ = os.WriteFile(other, []byte("other"), 0755)
	_ = os.Rename(other, path)
	if err := replaceCLI(path, []byte("upgrade"), before); err == nil {
		t.Fatal("replaced a concurrently changed installation")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "other" {
		t.Fatal(string(raw))
	}
}
