package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

func runWorker(c config) report {
	return runWorkerContext(context.Background(), c)
}

func runWorkerContext(parent context.Context, c config) (result report) {
	start := time.Now()
	result.Model = c.Model
	result.AuthMode = authenticationMode(c)
	result.Scenario = c.Scenario
	result.CandidateVersion = c.CandidateVersion
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
	ctx, cancel := context.WithTimeout(parent, 8*time.Minute)
	defer cancel()
	gitEnv := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
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
	f := &fixture{Approved: true}
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
	env := []string{"HOME=" + home, "PATH=" + filepath.Join(home, ".local/bin") + string(os.PathListSeparator) + os.Getenv("PATH"), "DOOMER_API_URL=" + server.URL, "DOOMER_CONFIG_DIR=" + filepath.Join(home, ".config/doomer"), "DOOMER_TOKEN=fixture-account-token", "NO_COLOR=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	browserPath := filepath.Join(home, "opened-browser-url")
	browserScript := fmt.Sprintf("#!/usr/bin/env python3\nimport pathlib, sys\npathlib.Path(%q).write_text(sys.argv[1])\n", browserPath)
	if err := os.MkdirAll(filepath.Join(home, ".local/bin"), 0700); err != nil {
		return fail(err)
	}
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(home, ".local/bin", name), []byte(browserScript), 0700); err != nil {
			return fail(err)
		}
	}
	env = append(env, "DISPLAY=fixture")
	_, err = configureAgent(c, home, proxyURL)
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
	if authenticationMode(c) == "subscription" && c.Harness == "claude" {
		if err := verifyClaudeSubscription(ctx, c, repo, env); err != nil {
			return fail(err)
		}
		result.Checks["subscription_authentication"] = true
	}

	j := journey{c: c, ctx: ctx, home: home, repo: repo, env: env, f: f, result: &result, record: record}
	if err := j.onboarding(); err != nil {
		return fail(err)
	}
	for name, passed := range result.Checks {
		if !passed {
			return fail(fmt.Errorf("assertion failed: %s", name))
		}
	}
	result.Passed = true
	return result
}

type httpHandler func(http.ResponseWriter, *http.Request)

func (h httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h(w, r) }

var anonymousCredential = regexp.MustCompile(`doomer_anon_[A-Za-z0-9_-]+`)

func redact(text, key string) string {
	if key != "" {
		text = strings.ReplaceAll(text, key, "[REDACTED]")
	}
	text = strings.ReplaceAll(text, "fixture-account-token", "[TEST TOKEN]")
	return anonymousCredential.ReplaceAllString(text, "[TEST ANONYMOUS TOKEN]")
}
