package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const agentInstructionsStart = "<!-- doomer-review:start -->"
const agentInstructionsEnd = "<!-- doomer-review:end -->"
const agentInstructions = `<!-- doomer-review:start -->
## Doomer code review

Use the installed doomer-review skill when editing code. Doomer's trusted hooks
capture tasks and edits and deliver review results; do not poll review state or
run setup on every edit. Investigate feedback privately, fix confirmed issues
within the user's scope, and record each response using the hook's instructions.
Include the complete Doomer review link once in the final answer, including when
no issues remain. Only report completion when the hook confirms that revision.
Repository upload consent and coding-agent hook trust are separate user choices.
If setup is missing, follow the skill's onboarding instructions. Never approve
uploads or hook trust on the user's behalf. Preserve a paused watcher.
<!-- doomer-review:end -->`

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
		text = text[:start] + agentInstructions + text[end+len(agentInstructionsEnd):]
	} else {
		text = strings.TrimRight(text, "\n") + "\n\n" + agentInstructions + "\n"
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
