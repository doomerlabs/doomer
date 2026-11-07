package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
)

func authenticationMode(c config) string {
	if c.AuthMode == "" {
		return "api"
	}
	return c.AuthMode
}

func validateAuthMode(harness, mode string) error {
	if mode != "api" && mode != "subscription" {
		return fmt.Errorf("authentication mode must be api or subscription")
	}
	if mode == "subscription" && harness != "claude" {
		return fmt.Errorf("subscription authentication currently supports Claude only")
	}
	return nil
}

func validateAuthentication(c config) error {
	if err := validateAuthMode(c.Harness, authenticationMode(c)); err != nil {
		return err
	}
	if authenticationMode(c) == "subscription" {
		if c.OAuthToken == "" {
			return fmt.Errorf("set TEST_CLAUDE_CODE_OAUTH_TOKEN for subscription mode")
		}
		if c.APIKey != "" {
			return fmt.Errorf("subscription mode must not receive an API key")
		}
		return nil
	}
	if c.APIKey == "" {
		if c.Harness == "claude" {
			return fmt.Errorf("set TEST_ANTHROPIC_API_KEY for API mode")
		}
		return fmt.Errorf("set TEST_OPENAI_API_KEY for API mode")
	}
	if c.OAuthToken != "" {
		return fmt.Errorf("API mode must not receive a subscription token")
	}
	return nil
}

// The worker builds a fresh environment, excluding the parent's provider keys
// and endpoints. Each mode supplies exactly one provider authentication route.
func agentAuthenticationEnvironment(c config, home, proxyURL string) []string {
	if c.Harness != "claude" {
		return []string{"CODEX_HOME=" + filepath.Join(home, ".codex"), "OPENAI_API_KEY=fixture-proxy-token"}
	}
	env := []string{"CLAUDECODE=1", "CLAUDE_CONFIG_DIR=" + filepath.Join(home, ".claude"), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1", "DISABLE_UPDATES=1"}
	if authenticationMode(c) == "subscription" {
		return append(env, "CLAUDE_CODE_OAUTH_TOKEN="+c.OAuthToken)
	}
	return append(env, "ANTHROPIC_AUTH_TOKEN=fixture-proxy-token", "ANTHROPIC_BASE_URL="+proxyURL)
}

func verifyClaudeSubscription(ctx context.Context, c config, repo string, env []string) error {
	cmd := exec.CommandContext(ctx, c.Codex, "auth", "status")
	cmd.Dir, cmd.Env = repo, env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("Claude subscription authentication check failed: %w: %s", err, redact(redact(stderr.String(), c.OAuthToken), c.APIKey))
	}
	var status struct {
		LoggedIn bool   `json:"loggedIn"`
		Method   string `json:"authMethod"`
		Provider string `json:"apiProvider"`
	}
	if json.Unmarshal(raw, &status) != nil || !status.LoggedIn || status.Method != "oauth_token" || status.Provider != "firstParty" {
		return fmt.Errorf("Claude did not confirm the expected subscription OAuth authentication")
	}
	return nil
}
