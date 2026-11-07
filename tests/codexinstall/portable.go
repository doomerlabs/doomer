package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (j *journey) portableRestart() error {
	fresh := filepath.Join(filepath.Dir(j.home), "restarted-home")
	if err := os.MkdirAll(filepath.Join(fresh, ".codex"), 0700); err != nil {
		return err
	}
	// Only the dummy model-provider configuration moves. Doomer must restore
	// account and repository permission itself from the approved portable grant.
	if err := os.WriteFile(filepath.Join(fresh, ".codex/config.toml"), []byte(j.codexConfig), 0600); err != nil {
		return err
	}
	old := j.home
	for i, value := range j.env {
		j.env[i] = strings.ReplaceAll(value, old, fresh)
	}
	cmd := exec.CommandContext(j.ctx, filepath.Join(old, ".local/bin/doomer"), "install", "--runtime", "--repo", j.repo)
	cmd.Dir, cmd.Env = j.repo, j.env
	output, err := cmd.CombinedOutput()
	j.record("Command", "Install software into a fresh home, preserving Git metadata\n"+string(output))
	if err != nil {
		return fmt.Errorf("fresh-home software install: %w", err)
	}
	if j.c.CandidateFile != "" {
		if err := installCandidate(j.c, fresh); err != nil {
			return err
		}
	}
	j.home = fresh
	j.record("Fixture", "Started a new environment with an empty Doomer account store. No Doomer credentials or receipts were copied.")
	if err := j.coding(j.repo, "Create restored.txt containing restored followed by a newline. Use the installed Doomer workflow and finish its review."); err != nil {
		return err
	}
	j.result.Checks["fresh_home_reused_grant"] = true
	return j.finish()
}
