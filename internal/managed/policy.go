// Package managed owns the bootstrap contract and durable, per-call runtime.
package managed

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"go.yaml.in/yaml/v3"
)

const Protocol = 2

type Policy struct {
	Protocol   int    `yaml:"bootstrap-protocol" json:"bootstrap_protocol"`
	Channel    string `yaml:"update-policy" json:"update_policy"`
	Version    string `yaml:"version,omitempty" json:"version,omitempty"`
	DelayHours int    `yaml:"delay-hours,omitempty" json:"delay_hours,omitempty"`
}

type Bootstrap struct {
	Path     string `json:"path"`
	Scope    string `json:"scope"`
	Policy   Policy `json:"policy"`
	Revision string `json:"revision,omitempty"`
	CLIRange string `json:"cli_range,omitempty"`
}

func (p Policy) Validate() error {
	if p.Protocol != Protocol {
		return fmt.Errorf("bootstrap protocol %d is unsupported; expected %d", p.Protocol, Protocol)
	}
	switch p.Channel {
	case "stable", "manual":
		if p.Version != "" || p.DelayHours != 0 {
			return fmt.Errorf("%s policy cannot specify version or delay-hours", p.Channel)
		}
	case "delayed":
		if p.DelayHours < 1 || p.DelayHours > 8760 || p.Version != "" {
			return fmt.Errorf("delayed policy requires delay-hours between 1 and 8760")
		}
	case "pinned":
		if !runtimeversion.Release.MatchString(p.Version) || p.DelayHours != 0 {
			return fmt.Errorf("pinned policy requires an exact stable version")
		}
	default:
		return fmt.Errorf("unknown update-policy %q", p.Channel)
	}
	return nil
}

func ReadBootstrap(path, scope string) (Bootstrap, error) {
	b := Bootstrap{Path: path, Scope: scope}
	file, err := os.Open(path)
	if err != nil {
		return b, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil {
		return b, err
	}
	if len(raw) > 64<<10 {
		return b, fmt.Errorf("bootstrap exceeds 64 KiB")
	}
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	parts := bytes.SplitN(raw, []byte("\n---\n"), 2)
	if len(parts) != 2 || !bytes.HasPrefix(parts[0], []byte("---\n")) {
		return b, fmt.Errorf("bootstrap requires YAML frontmatter")
	}
	var header struct {
		Name     string            `yaml:"name"`
		Metadata map[string]string `yaml:"metadata"`
	}
	if err := yaml.Unmarshal(parts[0][4:], &header); err != nil {
		return b, err
	}
	if header.Name != "doomer-review" {
		return b, fmt.Errorf("unexpected bootstrap name")
	}
	protocol, err := strconv.Atoi(header.Metadata["doomer-protocol"])
	if err != nil {
		return b, fmt.Errorf("invalid doomer-protocol metadata")
	}
	delay := 0
	if value := header.Metadata["doomer-delay-hours"]; value != "" {
		delay, err = strconv.Atoi(value)
		if err != nil {
			return b, fmt.Errorf("invalid doomer-delay-hours metadata")
		}
	}
	b.Policy = Policy{Protocol: protocol, Channel: header.Metadata["doomer-update-policy"], Version: header.Metadata["doomer-version"], DelayHours: delay}
	b.Revision = header.Metadata["doomer-bootstrap-version"]
	b.CLIRange = header.Metadata["doomer-cli-range"]
	if b.Revision != "" && !runtimeversion.Release.MatchString(b.Revision) {
		return b, fmt.Errorf("invalid doomer-bootstrap-version metadata")
	}
	if b.CLIRange != "" {
		if _, err := BootstrapSupports(b.CLIRange, BootstrapVersion); err != nil {
			return b, err
		}
	}
	return b, b.Policy.Validate()
}

// Repository policy wins. Both repository copies must agree regardless of
// which agent is active. Personal copies are only consulted when neither exists.
func ResolveBootstrap(repo, home, agent string) (Bootstrap, error) {
	if agent != "codex" && agent != "claude" {
		return Bootstrap{}, fmt.Errorf("agent must be codex or claude")
	}
	var selected *Bootstrap
	for _, dir := range []string{".agents", ".claude"} {
		path := filepath.Join(repo, dir, "skills/doomer-review/SKILL.md")
		b, err := ReadBootstrap(path, "project")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return Bootstrap{}, err
		}
		if selected != nil && selected.Policy != b.Policy {
			return Bootstrap{}, fmt.Errorf("repository bootstrap policies disagree")
		}
		if selected == nil {
			selected = &b
		}
	}
	if selected != nil {
		return *selected, nil
	}
	dir := ".agents"
	if agent == "claude" {
		dir = ".claude"
	}
	return ReadBootstrap(filepath.Join(home, dir, "skills/doomer-review/SKILL.md"), "personal")
}

func (p Policy) Eligible(r Release, now time.Time) bool {
	if p.Channel == "pinned" {
		return r.Version == p.Version
	}
	return !r.Published.After(now.Add(-time.Duration(p.DelayHours)*time.Hour)) && !r.Withdrawn
}

func BootstrapText(p Policy) (string, error) {
	return bootstrapText(p, bootstrapTemplate, BootstrapVersion, BootstrapCLIRange)
}

// Personal installs carry their own discovery trigger without changing the
// repository bootstrap distributed alongside AGENTS.md and CLAUDE.md.
func BootstrapTextForScope(p Policy, scope string) (string, error) {
	if scope != "personal" {
		return BootstrapText(p)
	}
	return bootstrapText(p, personalBootstrapTemplate(bootstrapTemplate), BootstrapVersionForScope(scope), BootstrapCLIRange)
}

func personalBootstrapTemplate(template string) string {
	return strings.Replace(template,
		"description: Review edited source with Doomer and reuse approved setup.",
		"description: >-\n  Use Doomer for every coding task that creates or modifies source code, fixes\n  bugs, refactors code, or changes tests or build configuration. Start before\n  editing and complete the review workflow before delivering changes, even\n  when the user does not explicitly request code review.", 1)
}

func bootstrapText(p Policy, template, revision, cliRange string) (string, error) {
	// Historical wrappers must retain the instructions users actually installed.
	if revision != BootstrapVersion && revision != personalBootstrapVersion {
		template = strings.ReplaceAll(template, "{{upgrade_prompt_rules}}", strings.TrimSpace(legacyUpgradePromptRules))
		template = strings.ReplaceAll(template, "{{final_review_rules}}", strings.TrimSpace(legacyFinalReviewRules))
	}
	if err := p.Validate(); err != nil {
		return "", err
	}
	metadata := map[string]string{"doomer-protocol": strconv.Itoa(p.Protocol), "doomer-update-policy": p.Channel}
	if revision != "" {
		metadata["doomer-bootstrap-version"] = revision
		metadata["doomer-cli-range"] = cliRange
	}
	if p.Version != "" {
		metadata["doomer-version"] = p.Version
	}
	if p.DelayHours != 0 {
		metadata["doomer-delay-hours"] = strconv.Itoa(p.DelayHours)
	}
	data, err := yaml.Marshal(metadata)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		lines = append(lines, "  "+line)
	}
	return expandPromptRules(strings.Replace(template, "{{metadata}}", strings.Join(lines, "\n"), 1)), nil
}
