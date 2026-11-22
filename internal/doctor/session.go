package doctor

import (
	"fmt"
	"strings"
)

// SessionPrompt is the full prompt matched by the installed prompt hook.
// Keep it identical to the copyable prompt in the troubleshooting UI.
const SessionPrompt = `Doomer Doctor: Test this session

Please check the Quick review hook and watcher in this chat without editing files or sending source. Report only the live Doctor result from this turn, including the last confirmed step and next action. If no live Doctor result arrived, say “No hook receipt for this session” and ask me to check that the Doomer prompt hook is loaded and trusted in this agent profile, then retry this prompt.`

func IsSessionPrompt(prompt string) bool {
	return strings.TrimSpace(strings.ReplaceAll(prompt, "\r\n", "\n")) == SessionPrompt
}

type SessionResult struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	Last  string `json:"last_confirmed_step"`
	Next  string `json:"next"`
}

func SessionCheck(code string) SessionResult {
	switch code {
	case "watcher_ready":
		return SessionResult{code, "Hook and watcher verified", "Watcher initialized for this checkout", "Run a first Quick review with a small edit after confirming upload approval."}
	case "checkout_unavailable":
		return SessionResult{code, "Git checkout unavailable", "Agent called the hook", "Open the affected Git checkout in this agent and retry the same prompt."}
	case "upload_approval_missing":
		return SessionResult{code, "Checkout upload approval required", "Agent called the hook", "Check this checkout’s upload scope for the selected service, then retry the same prompt."}
	case "watcher_credentials_unavailable":
		return SessionResult{code, "Watcher credentials unavailable", "Agent called the hook", "Check the selected account and profile with doomer doctor --no-upload, then retry the same prompt."}
	default:
		return SessionResult{"watcher_startup_unverified", "Watcher startup unverified", "Agent called the hook", "Run doomer doctor --no-upload in this checkout to inspect the installation and watcher settings."}
	}
}

func (r SessionResult) String() string {
	return fmt.Sprintf("Doomer Doctor session test\n%s [%s]\nLast confirmed step: %s\nNext: %s\nNo source or report was sent. The test ends before review submission and chat delivery.", r.Label, r.Code, r.Last, r.Next)
}
