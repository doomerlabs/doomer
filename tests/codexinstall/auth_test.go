package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAuthenticationModesStaySeparate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config config
		valid  bool
	}{
		{"legacy API", config{APIKey: "api"}, true},
		{"Claude API", config{Harness: "claude", AuthMode: "api", APIKey: "api"}, true},
		{"Claude subscription", config{Harness: "claude", AuthMode: "subscription", OAuthToken: "oauth"}, true},
		{"missing subscription token", config{Harness: "claude", AuthMode: "subscription", APIKey: "api"}, false},
		{"mixed subscription credentials", config{Harness: "claude", AuthMode: "subscription", OAuthToken: "oauth", APIKey: "api"}, false},
		{"mixed API credentials", config{Harness: "claude", AuthMode: "api", OAuthToken: "oauth", APIKey: "api"}, false},
		{"unsupported Codex subscription", config{Harness: "codex", AuthMode: "subscription", OAuthToken: "oauth"}, false},
		{"unknown mode", config{AuthMode: "other", APIKey: "api"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateAuthentication(tc.config); (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func TestSubscriptionEnvironmentHasNoAPIAuthentication(t *testing.T) {
	c := config{Harness: "claude", AuthMode: "subscription", OAuthToken: "subscription-secret"}
	env := strings.Join(agentAuthenticationEnvironment(c, "/fixture", "http://proxy.invalid"), "\n")
	if !strings.Contains(env, "CLAUDE_CODE_OAUTH_TOKEN=subscription-secret") {
		t.Fatal("missing subscription token")
	}
	for _, field := range []string{"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "ANTHROPIC_BASE_URL=", "OPENAI_API_KEY="} {
		if strings.Contains(env, field) {
			t.Fatal("subscription environment includes API setting", field)
		}
	}
	api := strings.Join(agentAuthenticationEnvironment(config{Harness: "claude", APIKey: "api"}, "/fixture", "http://proxy.invalid"), "\n")
	if strings.Contains(api, "CLAUDE_CODE_OAUTH_TOKEN=") || !strings.Contains(api, "ANTHROPIC_BASE_URL=http://proxy.invalid") {
		t.Fatal(api)
	}
}

func TestSubscriptionTokenIsRedactedFromRecording(t *testing.T) {
	secret := "subscription-secret"
	if got := redact("token="+secret, secret); strings.Contains(got, secret) {
		t.Fatal(got)
	}
}

func TestVerifyClaudeSubscriptionRejectsOtherAuthentication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"subscription", `{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty"}`, true},
		{"API key", `{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty"}`, false},
		{"logged out", `{"loggedIn":false,"authMethod":"oauth_token","apiProvider":"firstParty"}`, false},
		{"gateway", `{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"gateway"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "claude")
			if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s' '"+tc.body+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			err := verifyClaudeSubscription(context.Background(), config{Codex: path}, t.TempDir(), []string{"PATH=/usr/bin:/bin"})
			if (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}

func TestClaudeProviderFailureAppearsInReportError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	path := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"401 Invalid bearer token"}]}}' '{"type":"result","is_error":true,"result":"401 Invalid bearer token"}'
exit 1
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := runClaude(context.Background(), config{Codex: path}, t.TempDir(), []string{"PATH=/usr/bin:/bin"}, "hello", "", func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "401 Invalid bearer token") {
		t.Fatal(err)
	}
}
