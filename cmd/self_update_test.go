package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSelfUpdateCheckIgnoresAuthAndWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOOMER_CONFIG_DIR", filepath.Join(home, "invalid-config"))
	t.Setenv("DOOMER_API_URL", "not-a-url")
	c := newRootCommand(nil)
	c.SetArgs([]string{"self-update", "--check", "--version", "2026.11.3.5"})
	var out bytes.Buffer
	c.SetOut(&out)
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	var status struct{ State string }
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.State != "missing" {
		t.Fatal(status)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 0 {
		t.Fatal("status check changed home")
	}
}
func TestSelfUpdateRequiresPinnedVersion(t *testing.T) {
	c := newRootCommand(nil)
	c.SetArgs([]string{"self-update", "--check"})
	if err := c.Execute(); err == nil {
		t.Fatal("unpinned update accepted")
	}
}
