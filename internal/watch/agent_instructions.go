package watch

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/internal/version"
)

const agentInstructionsStart = "<!-- doomer-review:start -->"
const agentInstructionsEnd = "<!-- doomer-review:end -->"

//go:embed templates/agent-instructions.md
var agentInstructionsBody string

var agentInstructions = agentInstructionsStart + "\n" + agentInstructionsBody + agentInstructionsEnd

func installedAgentInstructions() string {
	if len(version.Commit) != 40 || strings.IndexFunc(version.Commit, func(r rune) bool { return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') }) >= 0 {
		return agentInstructions
	}
	marker := fmt.Sprintf("<!-- doomer-managed\npackage: doomerlabs/doomer\nrevision: %s\nsource: internal/watch/templates/agent-instructions.md\nkind: block\n-->\n", version.Commit)
	return agentInstructionsStart + "\n" + marker + agentInstructionsBody + "<!-- doomer-managed:end -->\n" + agentInstructionsEnd
}

// Update only our marked block. Never follow a repository-provided symlink or
// overwrite malformed markers that might enclose unrelated instructions.
func EnsureAgentInstructions(repo, agent string) error {
	name := "AGENTS.md"
	if agent == "claude" {
		name = "CLAUDE.md"
	} else if agent != "codex" {
		return fmt.Errorf("unsupported coding agent: %s", agent)
	}
	file := filepath.Join(repo, name)
	mode := os.FileMode(0644)
	if info, err := os.Lstat(file); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular %s", name)
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(data)
	// A new CLAUDE.md must not hide existing shared repository instructions.
	if os.IsNotExist(err) && agent == "claude" {
		if info, statErr := os.Lstat(filepath.Join(repo, "AGENTS.md")); statErr == nil && info.Mode().IsRegular() {
			text = "@AGENTS.md\n"
		}
	}
	start, end := strings.Index(text, agentInstructionsStart), strings.Index(text, agentInstructionsEnd)
	if start >= 0 || end >= 0 {
		if start < 0 || end < start || strings.Count(text, agentInstructionsStart) != 1 || strings.Count(text, agentInstructionsEnd) != 1 {
			return fmt.Errorf("%s contains malformed Doomer markers", name)
		}
		text = text[:start] + installedAgentInstructions() + text[end+len(agentInstructionsEnd):]
	} else {
		text = strings.TrimRight(text, "\n") + "\n\n" + installedAgentInstructions() + "\n"
		text = strings.TrimLeft(text, "\n")
	}
	if text == string(data) {
		return nil
	}
	temp, err := os.CreateTemp(repo, ".doomer-agents-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err = temp.Chmod(mode); err == nil {
		_, err = temp.WriteString(text)
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp.Name(), file)
}
