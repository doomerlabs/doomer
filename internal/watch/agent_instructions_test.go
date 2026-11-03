package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentInstructionsPreserveUserContentAndAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "AGENTS.md")
	os.WriteFile(file, []byte("User rules\n"), 0600)
	if err := EnsureAgentInstructions(dir); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(first), "User rules\n") || !strings.Contains(string(first), "do not poll") {
		t.Fatal(string(first))
	}
	if err := EnsureAgentInstructions(dir); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(file)
	if string(first) != string(second) {
		t.Fatal("not idempotent")
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 {
		t.Fatal("mode changed")
	}
	os.WriteFile(file, []byte("User rules\n"+agentInstructionsStart), 0600)
	if EnsureAgentInstructions(dir) == nil {
		t.Fatal("malformed markers overwritten")
	}
	os.Remove(file)
	target := filepath.Join(t.TempDir(), "rules")
	os.WriteFile(target, []byte("original"), 0600)
	os.Symlink(target, file)
	if EnsureAgentInstructions(dir) == nil {
		t.Fatal("followed symlink")
	}
}
