package runtimeversion

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinRequiresOneConsistentRelease(t *testing.T) {
	root := t.TempDir()
	put := func(agent, body string) {
		t.Helper()
		path := filepath.Join(root, agent, "skills/doomer-review/references")
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "install.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if v, _, err := Pin(root); err != nil || v != "" {
		t.Fatal(v, err)
	}
	put(".agents", "## Select CLI release 2026.11.3.7\n")
	if v, _, err := Pin(root); err != nil || v != "2026.11.3.7" {
		t.Fatal(v, err)
	}
	put(".claude", "## Select CLI release 2026.11.4.1\n")
	if _, _, err := Pin(root); err == nil {
		t.Fatal("accepted conflicting pins")
	}
	put(".claude", "## Select CLI release 2026.11.3.7\n")
	if _, _, err := Pin(root); err != nil {
		t.Fatal(err)
	}
	put(".agents", "## Select CLI release 2026.11.3.7\n## Select CLI release 2026.11.3.7\n")
	if _, _, err := Pin(root); err == nil {
		t.Fatal("accepted ambiguous release")
	}
}
