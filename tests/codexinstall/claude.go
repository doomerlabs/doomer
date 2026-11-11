package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const claudeVersion = "2.1.319"
const claudeEffort = "medium"
const codexEffort = "medium"

func claudePermissionMode(c config) string {
	if c.ReviewApprovals || c.ClaudePermissionMode == "" {
		return "auto"
	}
	return c.ClaudePermissionMode
}

const claudeRemoteInstall = `set -eu
mkdir -p /tmp/doomer-codex-e2e
cd /tmp/doomer-codex-e2e
curl -fsSL --retry 2 https://downloads.claude.ai/claude-code-releases/2.1.319/linux-x64/claude -o claude
printf '%s  claude\n' dabafec82ca1319fa43d8be36a10fc61b1d11821cb98ad5df04ad95e199b8eed | sha256sum -c -
chmod 755 claude
`

func agentName(c config) string {
	if c.Harness == "claude" {
		return "claude"
	}
	return "codex"
}
func agentHooksPath(c config, home string) string {
	if c.Harness == "claude" {
		return filepath.Join(home, ".claude", "settings.json")
	}
	return filepath.Join(home, ".codex", "hooks.json")
}
func validAgentVersion(c config, version string) bool {
	if c.Harness == "claude" {
		return version == claudeVersion+" (Claude Code)"
	}
	if c.AgentVersion != "" {
		return version == "codex-cli "+c.AgentVersion
	}
	return version == "codex-cli 0.167.0"
}
func configureAgent(c config, home, proxyURL string) (string, error) {
	configuration := fmt.Sprintf("model = %q\nmodel_provider = \"fixture\"\nmodel_reasoning_effort = %q\n[model_providers.fixture]\nname = \"Budgeted test proxy\"\nbase_url = %q\nenv_key = \"OPENAI_API_KEY\"\nwire_api = \"responses\"\n", c.Model, codexEffort, proxyURL+"/v1")
	if c.CodexAuthHome != "" {
		configuration = fmt.Sprintf("model = %q\nmodel_reasoning_effort = %q\n", c.Model, codexEffort)
		auth, err := os.ReadFile(filepath.Join(c.CodexAuthHome, "auth.json"))
		if err != nil {
			return "", err
		}
		if err = os.MkdirAll(filepath.Join(home, ".codex"), 0700); err != nil {
			return "", err
		}
		if err = os.WriteFile(filepath.Join(home, ".codex/auth.json"), auth, 0600); err != nil {
			return "", err
		}
	}
	if c.Harness == "claude" {
		configuration = `{ "hasCompletedOnboarding": true, "theme": "dark", "autoUpdates": false }`
	}
	return configuration, writeAgentConfig(c, home, configuration)
}
func writeAgentConfig(c config, home, configuration string) error {
	dir := filepath.Join(home, ".codex")
	path := filepath.Join(dir, "config.toml")
	if c.Harness == "claude" {
		dir = filepath.Join(home, ".claude")
		path = filepath.Join(dir, ".claude.json")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(configuration), 0600)
}
func runClaude(ctx context.Context, c config, repo string, env []string, prompt, session string, record func(string, string)) (codexTurn, error) {
	record("Scripted user", prompt)
	args := []string{"--print", "--verbose", "--output-format", "stream-json", "--permission-mode", claudePermissionMode(c), "--model", c.Model, "--effort", claudeEffort, "--max-turns", "60", "--append-system-prompt", "The active harness is Claude Code. Its Doomer agent identifier is claude."}
	if session != "" {
		args = append(args, "--resume", session)
	}
	cmd := exec.CommandContext(ctx, c.Codex, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = repo
	cmd.Env = env
	cmd.Stdin = strings.NewReader(prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return codexTurn{}, err
	}
	if err = cmd.Start(); err != nil {
		return codexTurn{}, err
	}
	type parsedTurn struct {
		turn codexTurn
		err  error
	}
	parsed := make(chan parsedTurn, 1)
	go func() {
		turn, err := readClaudeTurn(out, record)
		parsed <- parsedTurn{turn, err}
	}()
	var result parsedTurn
	select {
	case result = <-parsed:
	case <-ctx.Done():
		// A descendant can retain stdout after Claude exits. Close our reader
		// so cancellation also bounds transcript collection.
		_ = out.Close()
		result = <-parsed
	}
	turn, readErr := result.turn, result.err
	waitErr := cmd.Wait()
	if readErr != nil {
		return turn, readErr
	}
	if waitErr != nil || turn.Failed {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" && len(turn.Messages) > 0 {
			detail = turn.Messages[len(turn.Messages)-1]
		}
		return turn, fmt.Errorf("Claude execution failed: %s", detail)
	}
	return turn, nil
}
func readClaudeTurn(reader io.Reader, record func(string, string)) (codexTurn, error) {
	var turn codexTurn
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 65536), 4<<20)
	for scanner.Scan() {
		var event struct {
			Type    string          `json:"type"`
			Session string          `json:"session_id"`
			IsError bool            `json:"is_error"`
			Result  string          `json:"result"`
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return turn, err
		}
		if event.Session != "" {
			turn.Session = event.Session
		}
		if event.Type == "result" && event.IsError {
			turn.Failed = true
			record("Claude error", event.Result)
		}
		// Status and diagnostic events may use a string message. Only user and
		// assistant events carry the structured content blocks we need.
		if event.Type != "assistant" && event.Type != "user" {
			continue
		}
		var message struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(event.Message, &message); err != nil {
			return turn, fmt.Errorf("decode Claude %s message: %w", event.Type, err)
		}
		var blocks []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			Content json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(message.Content, &blocks)
		for _, block := range blocks {
			switch block.Type {
			case "text":
				if event.Type == "assistant" {
					turn.Messages = append(turn.Messages, block.Text)
					turn.PolicyOffered = turn.PolicyOffered || policyQuestion.MatchString(block.Text)
					record("Claude", block.Text)
				}
			case "tool_use":
				if block.Name == "Bash" {
					var input struct {
						Command string `json:"command"`
					}
					if json.Unmarshal(block.Input, &input) == nil {
						turn.Commands = append(turn.Commands, input.Command)
					}
				}
				record("Command", block.Name+" "+string(block.Input))
			case "tool_result":
				text := claudeContentText(block.Content)
				turn.UpdateRequested = turn.UpdateRequested || updateState.MatchString(text)
				turn.SetupPending = turn.SetupPending || claudeSetupPending(text)
				turn.PolicyOffered = turn.PolicyOffered || (policyProposal.MatchString(text) || claudePermissionProposal.MatchString(text))
				turn.HooksOffered = turn.HooksOffered || hookProposal.MatchString(text)
				record("Command", text)
			}
		}
	}
	return turn, scanner.Err()
}
func claudeContentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var texts []string
	for _, block := range blocks {
		if block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// Use the CLI's structured readiness state to drive the scripted user's local
// setup choice. The real agent's phrasing remains visible in the transcript.
func claudeSetupPending(text string) bool {
	type permission struct {
		State    string `json:"state"`
		Decision string `json:"decision"`
	}
	type entry struct {
		Permissions permission `json:"claude_permissions"`
	}
	type result struct {
		Permissions permission `json:"claude_permissions"`
		Saved       []entry    `json:"saved_consents"`
	}
	pending := func(p permission) bool {
		return p.State == "missing" && p.Decision != "declined" && p.Decision != "unreadable"
	}
	decode := func(raw string) bool {
		var r result
		if json.Unmarshal([]byte(raw), &r) != nil {
			return false
		}
		if pending(r.Permissions) {
			return true
		}
		for _, e := range r.Saved {
			if pending(e.Permissions) {
				return true
			}
		}
		return false
	}
	if decode(text) {
		return true
	}
	for _, line := range strings.Split(text, "\n") {
		if decode(line) {
			return true
		}
	}
	return false
}
