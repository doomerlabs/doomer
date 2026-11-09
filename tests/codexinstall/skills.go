package main

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Only the fixture receives these files. Production download verification is unchanged.
func loadCandidateSkills(c *config, source string) error {
	for path, target := range map[string]*string{
		"INSTALL.md":                    &c.SkillsGuide,
		"scripts/install.py":            &c.SkillsInstaller,
		"skills/doomer-review/SKILL.md": &c.SkillsBootstrap,
	} {
		raw, err := os.ReadFile(filepath.Join(source, path))
		if err != nil {
			return fmt.Errorf("read candidate skills %s: %w", path, err)
		}
		*target = string(raw)
	}
	if c.CandidateVersion != "" {
		c.SkillsBootstrap = regexp.MustCompile(`(?m)^  doomer-cli-range:.*$`).ReplaceAllString(c.SkillsBootstrap, "  doomer-cli-range: '>="+c.CandidateVersion+"'")
	}
	return nil
}

func writeCandidateSkills(c *config, root string) (string, error) {
	installer := filepath.Join(root, "candidate-install.py")
	if err := os.WriteFile(installer, []byte(c.SkillsInstaller), 0600); err != nil {
		return "", err
	}
	installerURL := (&url.URL{Scheme: "file", Path: installer}).String()
	scriptPattern := regexp.MustCompile(`https://raw\.githubusercontent\.com/doomerlabs/skills/[^/]+/scripts/install\.py`)
	digestPattern := regexp.MustCompile("`[a-f0-9]{64}`")
	if len(scriptPattern.FindAllString(c.SkillsGuide, -1)) != 1 || len(digestPattern.FindAllString(c.SkillsGuide, -1)) != 1 {
		return "", fmt.Errorf("candidate guide must identify one pinned installer URL and checksum")
	}
	guide := scriptPattern.ReplaceAllLiteralString(c.SkillsGuide, installerURL)
	guide = digestPattern.ReplaceAllLiteralString(guide, fmt.Sprintf("`%x`", sha256.Sum256([]byte(c.SkillsInstaller))))
	path := filepath.Join(root, "candidate-INSTALL.md")
	if err := os.WriteFile(path, []byte(guide), 0600); err != nil {
		return "", err
	}
	guideURL := (&url.URL{Scheme: "file", Path: path}).String()
	guideDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(guide)))
	if len(c.RepoBootstraps) != 0 {
		for agent, content := range c.RepoBootstraps {
			updated, err := bindCandidateGuide(content, guideURL, guideDigest)
			if err != nil {
				return "", err
			}
			c.RepoBootstraps[agent] = updated
		}
	} else {
		updated, err := bindCandidateGuide(c.SkillsBootstrap, guideURL, guideDigest)
		if err != nil {
			return "", err
		}
		c.SkillsBootstrap = updated
	}
	return guideURL, nil
}

// Preserve self-contained bootstraps and only rewrite an explicit guide pin.
func bindCandidateGuide(content, guideURL, guideDigest string) (string, error) {
	guidePattern := regexp.MustCompile(`https://raw\.githubusercontent\.com/doomerlabs/skills/[^/]+/INSTALL\.md`)
	digestPattern := regexp.MustCompile("`[a-f0-9]{64}`")
	matches := guidePattern.FindAllString(content, -1)
	if len(matches) == 0 {
		return content, nil
	}
	if len(matches) != 1 || len(digestPattern.FindAllString(content, -1)) != 1 {
		return "", fmt.Errorf("candidate bootstrap must identify one pinned guide URL and checksum")
	}
	bootstrap := guidePattern.ReplaceAllLiteralString(content, guideURL)
	return digestPattern.ReplaceAllLiteralString(bootstrap, "`"+guideDigest+"`"), nil
}

func installCandidateSkills(c config, home, repo string) error {
	for _, agent := range []string{".agents", ".claude"} {
		content := c.SkillsBootstrap
		if len(c.RepoBootstraps) != 0 {
			content = c.RepoBootstraps[agent]
		}
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("missing candidate bootstrap for %s", agent)
		}
	}
	root := repo
	if personalScenario(c.Scenario) {
		root = home
	}
	for _, agent := range []string{".agents", ".claude"} {
		path := filepath.Join(agent, "skills/doomer-review/SKILL.md")
		destination := filepath.Join(root, path)
		// Require real installation to have created the bootstrap before replacing it.
		if _, err := os.Stat(destination); err != nil {
			return fmt.Errorf("candidate bootstrap was not installed: %w", err)
		}
	}
	for _, agent := range []string{".agents", ".claude"} {
		destination := filepath.Join(root, agent, "skills/doomer-review/SKILL.md")
		content := c.SkillsBootstrap
		if len(c.RepoBootstraps) != 0 {
			content = c.RepoBootstraps[agent]
		}
		if err := os.WriteFile(destination, []byte(content), 0600); err != nil {
			return err
		}
	}
	return nil
}

// Keep the two agent copies separate so a change to either is exercised.
func loadRepositoryBootstraps(c *config, source string) error {
	c.RepoBootstraps = make(map[string]string)
	for _, agent := range []string{".agents", ".claude"} {
		raw, err := os.ReadFile(filepath.Join(source, agent, "skills/doomer-review/SKILL.md"))
		if err != nil {
			return fmt.Errorf("read repository bootstrap %s: %w", agent, err)
		}
		c.RepoBootstraps[agent] = string(raw)
	}
	return nil
}
