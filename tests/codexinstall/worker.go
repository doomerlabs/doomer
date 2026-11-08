package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func runWorker(c config) (result report) {
	start := time.Now()
	result.Model = c.Model
	result.AuthMode = authenticationMode(c)
	result.Scenario = c.Scenario
	result.CandidateVersion = c.CandidateVersion
	result.InstallGuide = c.InstallGuide
	var recordMu sync.Mutex
	result.Checks = map[string]bool{}
	record := func(kind, text string) {
		recordMu.Lock()
		defer recordMu.Unlock()
		result.Events = append(result.Events, recordedEvent{Seconds: time.Since(start).Seconds(), Kind: kind, Text: text})
	}
	defer func() {
		result.Duration = time.Since(start).Seconds()
		result.Error = redact(redact(result.Error, c.APIKey), c.OAuthToken)
		for i := range result.Events {
			result.Events[i].Text = redact(redact(result.Events[i].Text, c.APIKey), c.OAuthToken)
		}
	}()
	fail := func(err error) report { result.Error = err.Error(); return result }
	if err := validateAuthentication(c); err != nil {
		return fail(err)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fail(err)
	}
	if c.WorkRoot != "" {
		userHome = c.WorkRoot
	}
	root, err := os.MkdirTemp(userHome, "codex-install-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(root)
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	for _, dir := range []string{home, repo} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return fail(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	gitEnv := []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	git := exec.CommandContext(ctx, "git", "init", "-q", repo)
	git.Env = gitEnv
	if err = git.Run(); err != nil {
		return fail(err)
	}
	for _, args := range [][]string{{"config", "user.email", "fixture@example.invalid"}, {"config", "user.name", "Install fixture"}, {"-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "Fixture"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		cmd.Env = gitEnv
		if err = cmd.Run(); err != nil {
			return fail(err)
		}
	}
	f := &fixture{PortableChoice: c.Scenario == "portable-restart"}
	if c.Scenario == "portable-restart" {
		cmd := exec.CommandContext(ctx, "git", "-C", repo, "remote", "add", "origin", "https://github.com/example/portable-fixture.git")
		cmd.Env = gitEnv
		if err := cmd.Run(); err != nil {
			return fail(err)
		}
	}
	server := httptest.NewServer(httpHandler(f.serve))
	defer server.Close()
	m := newMeter(c)
	proxyURL := ""
	if authenticationMode(c) == "api" {
		proxy := httptest.NewUnstartedServer(httpHandler(m.serve))
		proxy.Config.BaseContext = func(net.Listener) context.Context { return ctx }
		proxy.Start()
		defer proxy.Close()
		proxyURL = proxy.URL
	}
	defer func() {
		// Cancel stalled inference before waiting for the final accounting lock.
		cancel()
		m.mu.Lock()
		result.Usage = m.usage
		m.mu.Unlock()
		f.mu.Lock()
		result.APICalls = append([]string(nil), f.Events...)
		f.mu.Unlock()
	}()
	env := []string{"HOME=" + home, "PATH=" + filepath.Join(home, ".local/bin") + ":/usr/bin:/bin:/usr/sbin:/sbin", "DOOMER_API_URL=" + server.URL, "DOOMER_CONFIG_DIR=" + filepath.Join(home, ".config/doomer"), "NO_COLOR=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	configuration, err := configureAgent(c, home, proxyURL)
	if err != nil {
		return fail(err)
	}
	env = append(env, agentAuthenticationEnvironment(c, home, proxyURL)...)
	version, err := exec.CommandContext(ctx, c.Codex, "--version").Output()
	if err != nil {
		return fail(err)
	}
	result.CodexVersion = strings.TrimSpace(string(version))
	if !validAgentVersion(c, result.CodexVersion) {
		return fail(fmt.Errorf("unexpected agent version: %s", result.CodexVersion))
	}
	if authenticationMode(c) == "subscription" {
		if err := verifyClaudeSubscription(ctx, c, repo, env); err != nil {
			return fail(err)
		}
		result.Checks["subscription_authentication"] = true
	}
	if c.SkillsGuide != "" {
		guide, err := writeCandidateSkills(c, root)
		if err != nil {
			return fail(err)
		}
		c.InstallGuide = guide
		result.InstallGuide = guide
		record("fixture", "Testing candidate skills installer, guide, and bootstrap")
	}
	if c.InstallGuide == "" {
		c.InstallGuide = "https://doomer.ai/install.md"
	}
	prompt := "Read " + c.InstallGuide + " and follow the instructions to set up Doomer in this repository. Use a team/repository installation, local-only without a PR. This is an isolated test: DOOMER_API_URL points at its local account service; preserve that setting. Ask before granting updates or repository uploads. Keep messages concise."
	prompt += " Use --agent " + agentName(c) + " when preparing Doomer and configuring this integration."
	if personalScenario(c.Scenario) {
		prompt = strings.Replace(prompt, "team/repository installation, local-only without a PR", "personal installation in my home directory, without a PR", 1)
	}
	if c.Harness == "claude" {
		prompt = "In this Claude Code session, read " + strings.TrimPrefix(prompt, "Read ")
	}
	session := ""
	approvedUpdate := false
	sawUpdateRequest := false
	declined := false
	candidateInstalled := false
	approvalLinkRetries := 0
	if c.CandidateFile != "" || c.SkillsGuide != "" {
		result.Turns++
		software, err := runAgent(ctx, c, repo, env, prompt+" Install the verified runtime and skill files only in this turn. Do not run doomer prepare or begin onboarding yet. Stop after the install command completes.", "", record)
		if err != nil {
			return fail(err)
		}
		session = software.Session
		if c.CandidateFile != "" {
			if err := installCandidate(c, home); err != nil {
				return fail(err)
			}
			candidateInstalled = true
			args := []string{"bootstrap", "upgrade", "--accept", "--repo", repo}
			if personalScenario(c.Scenario) {
				args = []string{"bootstrap", "upgrade", "--accept", "--personal"}
			}
			bootstrap := exec.CommandContext(ctx, c.CandidateFile, args...)
			bootstrap.Dir, bootstrap.Env = repo, env
			if output, err := bootstrap.CombinedOutput(); err != nil {
				return fail(fmt.Errorf("candidate bootstrap assembly: %w: %s", err, output))
			}
		}
		if c.SkillsBootstrap != "" || len(c.RepoBootstraps) != 0 {
			if err := installCandidateSkills(c, home, repo); err != nil {
				return fail(err)
			}
		}
		prompt = "The installed build has been checked. Continue onboarding Doomer in this repository using its existing installed files and original scope. This test home persists for the scenario, so use ordinary setup without --portable. Ask for the required user approvals. Use --agent " + agentName(c) + " when preparing Doomer and configuring this integration."
	}
	if c.Harness == "claude" && (c.CandidateFile != "" || c.SkillsGuide != "") {
		prompt = "In this Claude Code session, " + prompt
	}
	if c.Scenario == "portable-restart" {
		prompt = strings.Replace(prompt, "This test home persists for the scenario, so use ordinary setup without --portable.", "This scenario restarts the home, so use --portable.", 1)
		prompt += " I will choose reusable setup at the browser approval."
	}
	for turn := 0; turn < 6; turn++ {
		result.Turns++
		observed, err := runAgent(ctx, c, repo, env, prompt, session, record)
		if err != nil {
			return fail(err)
		}
		if observed.Session != "" {
			session = observed.Session
		}
		sawUpdateRequest = sawUpdateRequest || observed.UpdateRequested
		if observed.UpdateRequested && c.CandidateFile != "" && !candidateInstalled {
			if err := installCandidate(c, home); err != nil {
				return fail(err)
			}
			candidateInstalled = true
		}
		text := strings.Join(observed.Messages, "\n")
		if declined {
			f.mu.Lock()
			safe := !f.Approved && f.Uploads == 0
			creates := f.Creates
			f.mu.Unlock()
			if c.Scenario == "decline-updates" {
				safe = safe && creates == 0
			}
			if !safe {
				return fail(fmt.Errorf("declined permission was not respected"))
			}
			result.Checks["decline_respected"] = true
			result.Passed = true
			return result
		}
		ready, checks := inspectInstall(ctx, repo, home, env, personalScenario(c.Scenario), c.Harness)
		if ready {
			for name, ok := range checks {
				result.Checks[name] = ok
			}
			f.mu.Lock()
			result.Checks["one_repository_approval"] = f.Creates == 1 && f.Approved
			result.Checks["no_repository_upload"] = f.Uploads == 0
			result.Checks["asked_update_permission"] = approvedUpdate && sawUpdateRequest

			f.mu.Unlock()
			if !result.Checks["presented_approval_link"] {
				return fail(fmt.Errorf("repository approval link was not presented"))
			}
			for _, ok := range result.Checks {
				if !ok {
					return fail(fmt.Errorf("installation assertion failed"))
				}
			}
			versionCmd := exec.CommandContext(ctx, filepath.Join(home, ".local/bin/doomer"), "version")
			versionCmd.Env = env
			installedVersion, err := versionCmd.Output()
			if err != nil {
				return fail(fmt.Errorf("read installed Doomer version: %w", err))
			}
			result.DoomerVersion = strings.TrimSpace(string(installedVersion))
			if c.CandidateFile != "" && !candidateInstalled {
				if err := installCandidate(c, home); err != nil {
					return fail(err)
				}
			}
			j := journey{c: c, ctx: ctx, home: home, repo: repo, env: env, f: f, result: &result, record: record, codexConfig: configuration}
			if err := j.afterInstall(); err != nil {
				return fail(err)
			}
			result.Passed = true
			return result
		}
		f.mu.Lock()
		pending := f.Request != "" && !f.Approved
		approvalURL := server.URL + "/cli/setup#request=" + f.Request
		f.mu.Unlock()
		if pending {
			if !strings.Contains(text, approvalURL) {
				if approvalLinkRetries > 0 {
					return fail(fmt.Errorf("agent did not present the exact repository approval link after correction"))
				}
				approvalLinkRetries++
				record("Scripted user", "The approval link is missing or incorrect. Repository access has not been approved.")
				prompt = "The approval link is missing or incorrect. I have not approved repository access. Copy the exact complete approval URL from Doomer's output, preserving every character and the fragment."
				continue
			}
			f.mu.Lock()
			if c.Scenario == "decline-uploads" {
				f.Cancelled = true
				declined = true
			} else {
				f.Approved = true
			}
			f.mu.Unlock()
			result.Checks["presented_approval_link"] = true
			prompt = "I chose Allow on the repository approval page. Continue setup and verify it."
			if declined {
				prompt = "I declined repository uploads. Finish setup without uploads."
			}
			if c.Scenario == "interrupted" {
				tasks, e := savedTasks(home)
				if e != nil || len(tasks) != 1 {
					return fail(fmt.Errorf("missing durable setup task"))
				}
				session = ""
				prompt = "The original Doomer installation was interrupted. I approved repository access. This is the same persistent test home and repository; preserve DOOMER_API_URL and use ordinary setup without --portable. Run doomer prepare --task " + tasks[0].Handle + " to finish the original installation, including " + agentName(c) + " hook configuration. Verify setup without creating a replacement task."
				result.Checks["fresh_agent_resumed_setup"] = true
			}
		} else if sawUpdateRequest && !approvedUpdate && (strings.Contains(strings.ToLower(text), "update") || strings.Contains(strings.ToLower(text), "permission")) {
			approvedUpdate = true
			result.Checks["asked_update_permission"] = true
			prompt = "Allow automatic updates (recommended). Continue setup."
			if c.Scenario == "decline-updates" {
				declined = true
				prompt = "Not now. Do not enable updates or repository uploads. Finish with setup incomplete."
			}
		} else if checks["authenticated"] && !checks["hooks_configured"] && strings.Contains(strings.ToLower(text), "hook") {
			prompt = "I approve configuring the active agent's Doomer hooks as part of this installation. Preserve native trust controls. Complete setup and verify it."
		} else {
			return fail(fmt.Errorf("agent stopped before installation readiness without an expected approval request (checks: %v)", checks))
		}
	}
	return fail(fmt.Errorf("installation exceeded six conversation turns"))
}

type httpHandler func(http.ResponseWriter, *http.Request)

func (h httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h(w, r) }

func inspectInstall(ctx context.Context, repo, home string, env []string, personal bool, harness string) (bool, map[string]bool) {
	checks := map[string]bool{}
	base := repo
	if personal {
		base = home
	}
	for _, agent := range []string{".agents", ".claude"} {
		b, err := os.ReadFile(filepath.Join(base, agent, "skills/doomer-review/SKILL.md"))
		checks[agent+"_skill"] = err == nil && bytes.Contains(b, []byte("doomer-protocol"))
	}
	launcher := filepath.Join(home, ".local/bin/doomer")
	_, err := os.Stat(launcher)
	checks["launcher"] = err == nil
	hooks, _ := os.ReadFile(agentHooksPath(config{Harness: harness}, home))
	checks["hooks_configured"] = bytes.Contains(hooks, []byte("--managed-task-adapter"))
	if !checks["launcher"] {
		return false, checks
	}
	tasks, _ := filepath.Glob(filepath.Join(home, ".config/doomer/managed/tasks/*.json"))
	if len(tasks) != 1 {
		return false, checks
	}
	var task struct {
		Handle string `json:"handle"`
		Agent  string `json:"agent"`
	}
	data, _ := os.ReadFile(tasks[0])
	if json.Unmarshal(data, &task) != nil || task.Handle == "" {
		return false, checks
	}
	checks["correct_agent"] = task.Agent == agentName(config{Harness: harness})
	cmd := exec.CommandContext(ctx, launcher, "prepare", "--task", task.Handle)
	cmd.Dir = repo
	cmd.Env = env
	raw, err := cmd.Output()
	var state struct {
		State    string `json:"state"`
		Software bool   `json:"software_ready"`
		Auth     bool   `json:"authenticated"`
		Upload   bool   `json:"upload_approved"`
	}
	if err != nil || json.Unmarshal(raw, &state) != nil {
		return false, checks
	}
	checks["ready"] = state.State == "ready"
	checks["software_ready"] = state.Software
	checks["authenticated"] = state.Auth
	checks["upload_approved"] = state.Upload
	for _, ok := range checks {
		if !ok {
			return false, checks
		}
	}
	return true, checks
}

func redact(text, key string) string {
	if key != "" {
		text = strings.ReplaceAll(text, key, "[REDACTED]")
	}
	text = strings.ReplaceAll(text, "fixture-account-token", "[TEST TOKEN]")
	return text
}
