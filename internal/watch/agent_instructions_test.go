package watch

import (
	"github.com/doomerlabs/doomer/internal/version"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentInstructionsPreserveUserContentAndAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "AGENTS.md")
	os.WriteFile(file, []byte("User rules\n"), 0600)
	if err := EnsureAgentInstructions(dir, "codex"); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(first), "User rules\n") || !strings.Contains(string(first), "do not poll") {
		t.Fatal(string(first))
	}
	if err := EnsureAgentInstructions(dir, "codex"); err != nil {
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
	if EnsureAgentInstructions(dir, "codex") == nil {
		t.Fatal("malformed markers overwritten")
	}
	os.Remove(file)
	target := filepath.Join(t.TempDir(), "rules")
	os.WriteFile(target, []byte("original"), 0600)
	os.Symlink(target, file)
	if EnsureAgentInstructions(dir, "codex") == nil {
		t.Fatal("followed symlink")
	}
}

func TestClaudeInstructionsPreserveExistingRules(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "AGENTS.md")
	os.WriteFile(shared, []byte("Shared rules\n"), 0600)
	if err := EnsureAgentInstructions(dir, "claude"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "CLAUDE.md")
	first, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(first), "@AGENTS.md\n") || !strings.Contains(string(first), agentInstructionsStart) {
		t.Fatal(string(first))
	}
	if err := EnsureAgentInstructions(dir, "claude"); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(file)
	if string(first) != string(second) {
		t.Fatal("not idempotent")
	}
	original, _ := os.ReadFile(shared)
	if string(original) != "Shared rules\n" {
		t.Fatal("modified shared rules")
	}
	os.WriteFile(file, []byte("Claude rules\n"), 0600)
	os.Chmod(file, 0600)
	if err := EnsureAgentInstructions(dir, "claude"); err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(content), "Claude rules\n") {
		t.Fatal(string(content))
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 {
		t.Fatal("mode changed")
	}
	os.WriteFile(file, []byte(agentInstructionsStart), 0600)
	if EnsureAgentInstructions(dir, "claude") == nil {
		t.Fatal("overwrote malformed markers")
	}
	os.Remove(file)
	os.Symlink(shared, file)
	if EnsureAgentInstructions(dir, "claude") == nil {
		t.Fatal("followed symlink")
	}
}

func TestReleasedAgentInstructionsCarrySourceMarker(t *testing.T) {
	previous := version.Commit
	version.Commit = strings.Repeat("a", 40)
	defer func() { version.Commit = previous }()
	dir := t.TempDir()
	if err := EnsureAgentInstructions(dir, "codex"); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), "package: doomerlabs/doomer\nrevision: "+version.Commit) || !strings.Contains(string(first), "source: internal/watch/templates/agent-instructions.md") {
		t.Fatal(string(first))
	}
	if err := EnsureAgentInstructions(dir, "codex"); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if string(first) != string(second) {
		t.Fatal("repeated onboarding changed marked instructions")
	}
}
