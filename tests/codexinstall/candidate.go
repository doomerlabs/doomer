package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
)

// Candidate injection is restricted to this disposable fixture home. It lets
// real-agent tests exercise unreleased fixes after verifying public installation.
func installCandidate(c config, home string) error {
	data, err := os.ReadFile(c.CandidateFile)
	if err != nil {
		return err
	}
	path := runtimeversion.Binary(home, c.CandidateVersion)
	recordPath := filepath.Join(filepath.Dir(path), "installation.json")
	raw, err := os.ReadFile(recordPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("public installation did not produce the expected verified runtime %s; inspect the installation turn before injecting the candidate: %w", c.CandidateVersion, err)
	}
	if err != nil {
		return fmt.Errorf("read public installation record: %w", err)
	}
	var rec selfupdate.Record
	if err = json.Unmarshal(raw, &rec); err != nil {
		return err
	}
	if rec.Version != c.CandidateVersion {
		return fmt.Errorf("candidate version mismatch")
	}
	if err = os.WriteFile(path, data, 0700); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	rec.SHA256 = hex.EncodeToString(sum[:])
	raw, err = json.Marshal(rec)
	if err != nil {
		return err
	}
	if err = os.WriteFile(recordPath, raw, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, ".local/bin/doomer"), data, 0700)
}

// Assemble the candidate only inside the disposable fixture, after public
// installation. Published wrappers may differ from this checkout, so production
// bootstrap upgrade recognition must not gate fixture injection.
func assembleCandidateInstallation(c config, home, repo string) error {
	if c.CandidateFile != "" {
		if err := installCandidate(c, home); err != nil {
			return err
		}
		if c.SkillsBootstrap == "" && len(c.RepoBootstraps) == 0 {
			root, scope := repo, "project"
			if personalScenario(c.Scenario) {
				root, scope = home, "personal"
			}
			c.RepoBootstraps = make(map[string]string)
			for _, agent := range []string{".agents", ".claude"} {
				path := filepath.Join(root, agent, "skills/doomer-review/SKILL.md")
				installed, err := managed.ReadBootstrap(path, scope)
				if err != nil {
					return fmt.Errorf("read published bootstrap: %w", err)
				}
				content, err := managed.BootstrapText(installed.Policy)
				if err != nil {
					return err
				}
				c.RepoBootstraps[agent] = content
			}
		}
	}
	if c.SkillsBootstrap != "" || len(c.RepoBootstraps) != 0 {
		return installCandidateSkills(c, home, repo)
	}
	return nil
}
