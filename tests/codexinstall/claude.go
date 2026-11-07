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
	return version == "codex-cli 0.167.0"
}
func configureAgent(c config, home, proxyURL string) (string, error) {
	configuration := fmt.Sprintf("model = %q\nmodel_provider = \"fixture\"\nmodel_reasoning_effort = \"low\"\n[model_providers.fixture]\nname = \"Budgeted test proxy\"\nbase_url = %q\nenv_key = \"OPENAI_API_KEY\"\nwire_api = \"responses\"\n", c.Model, proxyURL+"/v1")
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
	args := []string{"--print", "--verbose", "--output-format", "stream-json", "--permission-mode", "bypassPermissions", "--model", c.Model, "--effort", "low", "--max-turns", "60", "--append-system-prompt", "The active harness is Claude Code. Its Doomer agent identifier is claude."}
	if session != "" {
		args = append(args, "--resume", session)
	}
	cmd := exec.CommandContext(ctx, c.Codex, args...)
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
	turn, readErr := readClaudeTurn(out, record)
	waitErr := cmd.Wait()
	if readErr != nil {
		return turn, readErr
	}
	if waitErr != nil || turn.Failed {
		return turn, fmt.Errorf("Claude execution failed: %s", strings.TrimSpace(stderr.String()))
	}
	return turn, nil
}
func readClaudeTurn(reader io.Reader, record func(string, string)) (codexTurn, error) {
	var turn codexTurn
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 65536), 4<<20)
	for scanner.Scan() {
		var event struct {
			Type    string `json:"type"`
			Session string `json:"session_id"`
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
			Message struct {
				Content []struct {
					Type    string          `json:"type"`
					Text    string          `json:"text"`
					Name    string          `json:"name"`
					Input   json.RawMessage `json:"input"`
					Content json.RawMessage `json:"content"`
				} `json:"content"`
			} `json:"message"`
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
		for _, block := range event.Message.Content {
			switch block.Type {
			case "text":
				if event.Type == "assistant" {
					turn.Messages = append(turn.Messages, block.Text)
					record("Claude", block.Text)
				}
			case "tool_use":
				record("Command", block.Name+" "+string(block.Input))
			case "tool_result":
				text := claudeContentText(block.Content)
				turn.UpdateRequested = turn.UpdateRequested || updateState.MatchString(text)
				turn.PolicyOffered = turn.PolicyOffered || policyProposal.MatchString(text)
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
