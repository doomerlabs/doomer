package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestNodeInstallationStatusAndUpgradeChannel(t *testing.T) {
	home, devHome := t.TempDir(), t.TempDir()
	t.Setenv("DOOMER_HOME", home)
	t.Setenv("DOOMER_DEV_HOME", devHome)
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(resolvedHome, "doomer")
	binary := []byte("node-managed binary")
	if err = os.WriteFile(path, binary, 0755); err != nil {
		t.Fatal(err)
	}
	receipt := func(dir, pkgVersion string) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"installer": "go", "cliVersion": pkgVersion,
			"native": map[string]string{"path": path, "sha256": fmt.Sprintf("%x", sha256.Sum256(binary))},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "state.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	receipt(home, "1.2.3-beta1")
	installation, err := identifyCLIInstallation(path)
	if err != nil || len(installation.Command) < 2 || installation.Command[1] != "@doomerlabs/doomer@beta" {
		t.Fatalf("unexpected beta installation: %+v (%v)", installation, err)
	}
	receipt(home, "")
	installation, err = identifyCLIInstallation(path)
	if err != nil || installation.Manager != "node" || len(installation.Command) != 0 || installation.upgradeBlocked == "" {
		t.Fatalf("unknown channel was not blocked: %+v (%v)", installation, err)
	}
	if err = os.Remove(filepath.Join(home, "state.json")); err != nil {
		t.Fatal(err)
	}
	receipt(devHome, "1.2.3-beta1")
	oldExecutable, oldRun := upgradeExecutable, upgradeRun
	t.Cleanup(func() { upgradeExecutable, upgradeRun = oldExecutable, oldRun })
	upgradeExecutable = func() (string, error) { return path, nil }
	upgradeRun = func(context.Context, *cobra.Command, []string) error {
		t.Fatal("development upgrade invoked the package manager")
		return nil
	}
	var out strings.Builder
	status := newStatusCommand()
	status.SetOut(&out)
	if err = status.Execute(); err != nil || !strings.Contains(out.String(), "Installation: node") {
		t.Fatalf("development status: %q (%v)", out.String(), err)
	}
	if err = newUpgradeCommand().Execute(); err == nil || !strings.Contains(err.Error(), "development build") {
		t.Fatalf("development upgrade: %v", err)
	}
}

func TestManagedExecutableAllowsSymlinkAliases(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "binary")
	if err := os.WriteFile(binary, []byte("managed binary"), 0755); err != nil {
		t.Fatal(err)
	}
	managed, alias := filepath.Join(root, "managed"), filepath.Join(root, "alias")
	if err := os.Symlink(binary, managed); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(managed, alias); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(alias)
	if err != nil || !sameInstallationPath(alias, resolved, managed) {
		t.Fatalf("managed alias was not recognized: %q (%v)", resolved, err)
	}
}

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

func TestUpgradeDoesNotAdoptUnrecognizedVersionedInstall(t *testing.T) {
	t.Setenv("DOOMER_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "doomer-2026.11.1.1")
	if err := os.WriteFile(path, []byte("versioned binary"), 0755); err != nil {
		t.Fatal(err)
	}
	installation, err := identifyCLIInstallation(path)
	if err != nil || installation.Manager != "unrecognized" {
		t.Fatal(installation, err)
	}
}

func TestSupportUploadRequiresExplicitUploadFlag(t *testing.T) {
	isolate(t)
	output := filepath.Join(t.TempDir(), "support.json")
	out, err := execute(t, "", "--api-url", "invalid", "support", "bundle", "--repo", doctorRepo(t), "--output", output)
	if err != nil || !strings.Contains(out, "Report saved") {
		t.Fatalf("bundle without upload must work despite invalid upload configuration: %s %v", out, err)
	}
	out, err = execute(t, "", "--api-url", "invalid", "support", "bundle", "--upload", "--report", output)
	if err == nil || !strings.Contains(err.Error(), "upload configuration is invalid") {
		t.Fatalf("explicit upload must validate the service: %s %v", out, err)
	}
}
