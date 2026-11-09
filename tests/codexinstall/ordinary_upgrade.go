package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
)

// Start from an existing installation, not a coached onboarding conversation.
// The old launcher and skill are real released files; only GitHub PR creation
// and the Doomer review service are local fixtures.
func (j *journey) ordinaryUpgrade() error {
	if j.c.Harness != "codex" {
		return fmt.Errorf("ordinary-upgrade currently targets the reported Codex regression")
	}
	updater := selfupdate.Updater{Home: j.home, Versioned: true}
	for _, v := range []string{"2026.11.8.7", j.c.CandidateVersion} {
		if _, err := updater.Apply(j.ctx, v, false); err != nil {
			return err
		}
	}
	old := runtimeversion.Binary(j.home, "2026.11.8.7")
	run := func(binary string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(j.ctx, binary, args...)
		cmd.Dir, cmd.Env = j.repo, j.env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("fixture command %v: %w: %s", args, err, out)
		}
		return out, nil
	}
	if _, err := run(old, "install", "--repo", j.repo); err != nil {
		return err
	}
	j.f.mu.Lock()
	j.f.Approved = true
	j.f.mu.Unlock()
	if _, err := run(old, "prepare", "--agent", "codex", "--repo", j.repo, "--allow-updates"); err != nil {
		return err
	}
	if err := installCandidate(j.c, j.home); err != nil {
		return err
	}
	bytes, err := os.ReadFile(old)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(j.home, ".local/bin/doomer"), bytes, 0700); err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(j.repo, "admin.ts"), []byte("export const adminLinks = ['Overview', 'Waitlist', 'Users'];\n"), 0600); err != nil {
		return err
	}
	if err = j.git(j.repo, "branch", "-M", "main"); err != nil {
		return err
	}
	if err = j.git(j.repo, "add", "."); err != nil {
		return err
	}
	if err = j.git(j.repo, "-c", "commit.gpgsign=false", "commit", "-qm", "Existing installation"); err != nil {
		return err
	}
	remote := filepath.Join(filepath.Dir(j.repo), "origin.git")
	if err = j.git(j.repo, "init", "--bare", remote); err != nil {
		return err
	}
	if err = j.git(j.repo, "remote", "add", "origin", remote); err != nil {
		return err
	}
	if err = j.git(j.repo, "push", "-u", "origin", "main"); err != nil {
		return err
	}
	if err = j.git(j.repo, "checkout", "-qb", "fixture/older-skill-pr"); err != nil {
		return err
	}
	if err = j.git(j.repo, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "Older pending skill update"); err != nil {
		return err
	}
	if err = j.git(j.repo, "push", "origin", "HEAD"); err != nil {
		return err
	}
	if err = j.git(j.repo, "checkout", "main"); err != nil {
		return err
	}
	marker := filepath.Join(j.home, "created-prs.jsonl")
	bin := filepath.Join(j.home, "fixture-bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		return err
	}
	shim := fmt.Sprintf(`#!/usr/bin/python3
import sys,json,subprocess,pathlib
args=sys.argv[1:]
old={"number":805,"title":"Update Doomer skill","headRefName":"fixture/older-skill-pr","baseRefName":"main","url":"https://example.invalid/pull/805","files":[{"path":".agents/skills/doomer-review/SKILL.md"},{"path":".claude/skills/doomer-review/SKILL.md"}]}
if args[:2]==['pr','create']:
 branch=subprocess.check_output(['git','branch','--show-current'],text=True).strip()
 files=subprocess.check_output(['git','diff','--name-only','origin/main...HEAD'],text=True).splitlines()
 record={'branch':branch,'files':files}
 p=pathlib.Path(%q)
 with p.open('a') as f:f.write(json.dumps(record)+'\n')
 print('https://example.invalid/pull/'+str(806+len(p.read_text().splitlines())))
elif args[:2]==['pr','list']:print(json.dumps([old]))
elif args[:2]==['pr','view']:print(json.dumps(old))
elif args[:2]==['repo','view']:print('main' if '--jq' in args else json.dumps({'nameWithOwner':'fixture/admin','defaultBranchRef':{'name':'main'}}))
elif args[:2]==['pr','diff']:print('Existing PR keeps bootstrap version 2026.11.8.12')
elif args[:2]==['auth','status']:print('Logged in to local PR fixture')
`, marker)
	if err = os.WriteFile(filepath.Join(bin, "gh"), []byte(shim), 0700); err != nil {
		return err
	}
	for i, v := range j.env {
		if strings.HasPrefix(v, "PATH=") {
			j.env[i] = "PATH=" + bin + ":" + strings.TrimPrefix(v, "PATH=")
		}
	}
	if j.c.Scenario == "policy-activation" {
		return j.policyActivation(run)
	}
	j.c.ReviewApprovals = true
	j.record("Fixture", "Real CLI 2026.11.8.7; skill 2026.11.8.12; saved grant; no reviewer policy/hooks; older pending skill PR")
	j.result.Turns++
	turn, err := runAgent(j.ctx, j.c, j.repo, j.env, "Remove the Waitlist section from the admin page and make a PR. We already removed the waitlist from the site.", "", j.record)
	if err != nil {
		return err
	}
	messages := strings.ToLower(strings.Join(turn.Messages, "\n"))
	j.result.Checks["ordinary_prompt_offers_skill_upgrade"] = strings.Contains(messages, "skill") && (strings.Contains(messages, "update") || strings.Contains(messages, "upgrade")) && strings.Contains(messages, j.c.CandidateVersion)
	if !j.result.Checks["ordinary_prompt_offers_skill_upgrade"] {
		return fmt.Errorf("ordinary prompt did not offer the available skill upgrade before completing the original request")
	}
	j.result.Checks["ordinary_prompt_offers_policy"] = strings.Contains(messages, "doomer repository review authorization") && strings.Contains(messages, "install")
	if !j.result.Checks["ordinary_prompt_offers_policy"] {
		return fmt.Errorf("ordinary prompt did not offer missing reviewer-policy setup")
	}
	j.result.Turns++
	next, err := runAgent(j.ctx, j.c, j.repo, j.env, "Yes, install the reviewer policy for the same account, service, profile and all-repository scope already approved. Preserve the existing upload grant and leave hooks unchanged. Then continue my request.", turn.Session, j.record)
	if err != nil {
		return err
	}
	messages += "\n" + strings.ToLower(strings.Join(next.Messages, "\n"))

	if !j.result.Checks["ordinary_prompt_offers_skill_upgrade"] {
		return fmt.Errorf("ordinary prompt did not offer the available skill upgrade")
	}
	j.result.Turns++
	_, err = runAgent(j.ctx, j.c, j.repo, j.env, "Yes, create the separate skill-update PR you offered and finish the original change.", next.Session, j.record)
	if err != nil {
		return err
	}
	prs, err := os.ReadFile(marker)
	if err != nil {
		return fmt.Errorf("requested PRs were not created: %w", err)
	}
	j.result.Checks["skill_upgrade_pr_created"] = false
	j.result.Checks["original_change_pr_created"] = false
	for _, line := range strings.Split(strings.TrimSpace(string(prs)), "\n") {
		var p struct {
			Branch string   `json:"branch"`
			Files  []string `json:"files"`
		}
		if json.Unmarshal([]byte(line), &p) != nil {
			continue
		}
		for _, path := range p.Files {
			if path == "admin.ts" && len(p.Files) == 1 {
				j.result.Checks["original_change_pr_created"] = true
			}
			if path == ".agents/skills/doomer-review/SKILL.md" {
				out, e := run("git", "show", p.Branch+":"+path)
				if e == nil && strings.Contains(string(out), "doomer-bootstrap-version: "+j.c.CandidateVersion) {
					j.result.Checks["skill_upgrade_pr_created"] = true
				}
			}
		}
	}
	// Updating the already-open, separate skill PR is equally valid.
	if !j.result.Checks["skill_upgrade_pr_created"] {
		out, showErr := run("git", "show", "origin/fixture/older-skill-pr:.agents/skills/doomer-review/SKILL.md")
		files, diffErr := run("git", "diff", "--name-only", "origin/main...origin/fixture/older-skill-pr")
		onlySkills := true
		for _, path := range strings.Fields(string(files)) {
			if path != ".agents/skills/doomer-review/SKILL.md" && path != ".claude/skills/doomer-review/SKILL.md" {
				onlySkills = false
			}
		}
		j.result.Checks["skill_upgrade_pr_created"] = showErr == nil && diffErr == nil && onlySkills && strings.Contains(string(out), "doomer-bootstrap-version: "+j.c.CandidateVersion)
	}
	policy, policyErr := os.ReadFile(filepath.Join(j.home, ".codex", "config.toml"))
	j.result.Checks["reviewer_policy_installed"] = policyErr == nil && strings.Contains(string(policy), "Doomer repository review authorization")
	j.f.mu.Lock()
	j.result.Checks["existing_upload_grant_reused"] = j.f.Creates == 1
	j.f.mu.Unlock()
	for name, ok := range j.result.Checks {
		if !ok {
			return fmt.Errorf("ordinary upgrade assertion failed: %s", name)
		}
	}
	return nil
}
