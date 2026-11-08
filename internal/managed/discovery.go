package managed

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const discoveryStart = "<!-- doomer-review:start -->"
const discoveryEnd = "<!-- doomer-review:end -->"

// RepositoryDiscoveryInstructions prepares the managed discovery block without
// writing files. Repository instructions never distribute credentials or hook trust.
// Keep the workflow in the skill so runtime updates need no instruction changes.
func RepositoryDiscoveryInstructions(repo, name string) ([]byte, []byte, error) {
	path := filepath.Join(repo, name)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("refusing non-regular instruction file: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	before, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	skill := ".agents/skills/doomer-review/SKILL.md"
	if name == "CLAUDE.md" {
		skill = ".claude/skills/doomer-review/SKILL.md"
	}
	block := discoveryStart + "\n## Doomer review\n\n" +
		"Before editing repository files, read and follow `" + skill + "`.\n" +
		"Read it directly from the checkout even if it is absent from the skill catalog.\n" +
		"Follow the skill to check local readiness and complete any missing onboarding\n" +
		"for the current coding agent, including required permissions and hook setup.\n" +
		"Reuse valid local setup; repository instructions do not grant upload permission\n" +
		"or hook trust. If the skill is missing, report the missing installation.\n" + discoveryEnd
	text := string(before)
	if before == nil && name == "CLAUDE.md" {
		text = "@AGENTS.md\n"
	}
	start, end := strings.Index(text, discoveryStart), strings.Index(text, discoveryEnd)
	if start >= 0 || end >= 0 {
		if start < 0 || end < start || strings.Count(text, discoveryStart) != 1 || strings.Count(text, discoveryEnd) != 1 {
			return nil, nil, fmt.Errorf("%s contains malformed Doomer instruction markers", name)
		}
		text = text[:start] + block + text[end+len(discoveryEnd):]
	} else {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if text != "" {
			text += "\n"
		}
		text += block + "\n"
	}
	return before, []byte(text), nil
}
