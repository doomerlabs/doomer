package main

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

var policyProposal = regexp.MustCompile(`"codex_review_policy"\s*:\s*\{`)

var policyQuestion = regexp.MustCompile(`(?is)\b(?:would you like|do you want|may I|can I|shall I|approve)\b.{0,120}(?:policy|reviewer settings|permissions?)`)

// A policy choice can name its subject before listing the answers rather than
// phrase the entire offer as "May I install the policy?".
func policyChoiceOffered(text string) bool {
	text = strings.ToLower(text)
	subject := strings.Contains(text, "policy") || strings.Contains(text, "reviewer settings") || strings.Contains(text, "scoped authorization")
	return subject && strings.Contains(text, "allow") && strings.Contains(text, "not now") && strings.Contains(text, "never ask again")
}

var claudePermissionProposal = regexp.MustCompile(`"claude_permissions"\s*:\s*\{[^}]*"install_command"`)

// Agents may extract the hook object from a large prepare response.
var hookProposal = regexp.MustCompile(`"hook_setup"\s*:\s*\{[^}]*"install_command"|"install_command"\s*:\s*\[[^\]]*"hook-setup"[^\]]*"--allow"`)

var updateState = regexp.MustCompile(`"state"\s*:\s*"update_permission_required"`)

type codexTurn struct {
	Session         string
	Commands        []string
	Messages        []string
	HooksOffered    bool
	PolicyOffered   bool
	SetupPending    bool
	UpdateRequested bool
	Failed          bool
}

func readCodexTurn(reader io.Reader, record func(string, string)) (codexTurn, error) {
	var turn codexTurn
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 65536), 4000000)
	for scanner.Scan() {
		var event struct {
			Type   string `json:"type"`
			Thread string `json:"thread_id"`
			Item   struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Command string `json:"command"`
				Output  string `json:"aggregated_output"`
			} `json:"item"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return turn, err
		}
		switch event.Type {
		case "thread.started":
			turn.Session = event.Thread
		case "turn.failed":
			turn.Failed = true
			record("Codex error", string(scanner.Bytes()))
		case "error":
			record("Codex error", string(scanner.Bytes()))
		case "item.started":
			if event.Item.Type == "command_execution" {
				record("Command started", event.Item.Command)
			}
		case "item.completed":
			switch event.Item.Type {
			case "command_execution":
				turn.Commands = append(turn.Commands, event.Item.Command)
				turn.UpdateRequested = turn.UpdateRequested || updateState.MatchString(event.Item.Output)
				turn.PolicyOffered = turn.PolicyOffered || policyProposal.MatchString(event.Item.Output)
				turn.HooksOffered = turn.HooksOffered || hookProposal.MatchString(event.Item.Output)
				record("Command", event.Item.Command+"\n"+event.Item.Output)
			case "agent_message":
				turn.Messages = append(turn.Messages, event.Item.Text)
				turn.PolicyOffered = turn.PolicyOffered || (policyQuestion.MatchString(event.Item.Text) || policyChoiceOffered(event.Item.Text))
				record("Codex", event.Item.Text)
			}
		}
	}
	return turn, scanner.Err()
}
