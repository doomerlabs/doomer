package main

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/doomerlabs/doomer/internal/managed"
)

// Installing a runtime must respect the existing npx-owned skill's policy.
// The CLI deliberately rejects an install request that would silently change it.
func globalRuntimeInstallArgs(home string) ([]string, error) {
	bootstrap, err := managed.ReadBootstrap(filepath.Join(home, ".agents/skills/doomer-review/SKILL.md"), "personal")
	if err != nil {
		return nil, err
	}
	args := []string{"install", "--runtime", "--personal", "--policy", bootstrap.Policy.Channel}
	if bootstrap.Policy.Version != "" {
		args = append(args, "--version", bootstrap.Policy.Version)
	}
	if bootstrap.Policy.DelayHours != 0 {
		args = append(args, "--delay-hours", strconv.Itoa(bootstrap.Policy.DelayHours))
	}
	return args, nil
}

// Upgrade scenarios start with historical stable-policy installations. Keep the
// initial fixture stable too, so replacing it with the historical wrapper does
// not change the consent policy halfway through the scenario. The public pinned
// skill is exercised unchanged by global-npx-discovery and ordinary scenarios.
func stableUpgradeFixture(c config) config {
	if !strings.HasPrefix(c.Scenario, "upgrade") {
		return c
	}
	stable := func(text string) string {
		header, body, found := strings.Cut(text, "\n---\n")
		if !found {
			return text
		}
		header = regexp.MustCompile(`(?m)^  doomer-update-policy:.*$`).ReplaceAllString(header, "  doomer-update-policy: stable")
		header = regexp.MustCompile(`(?m)^  doomer-(version|delay-hours):[^\n]*\n?`).ReplaceAllString(header, "")
		return strings.TrimRight(header, "\n") + "\n---\n" + body
	}
	c.SkillsBootstrap = stable(c.SkillsBootstrap)
	if c.RepoBootstraps != nil {
		copies := make(map[string]string, len(c.RepoBootstraps))
		for key, text := range c.RepoBootstraps {
			copies[key] = stable(text)
		}
		c.RepoBootstraps = copies
	}
	return c
}
