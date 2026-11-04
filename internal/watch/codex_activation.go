package watch

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

type codexHookReceipt struct {
	Repository string
	Owner      Owner
	At         time.Time
	Status     string
}

func codexHookReceiptPath(root, repo, session string) string {
	return filepath.Join(root, "codex-hook-receipts", Key(repo+"\x00"+session)+".json")
}

// Only the hook handler records receipts. Checking activation never creates one.
func recordCodexHook(root, repo string, owner Owner, status string) {
	_ = Write(codexHookReceiptPath(root, repo, owner.Session), codexHookReceipt{
		Repository: repo, Owner: owner, At: time.Now(), Status: status,
	})
}

// CodexActivationStatus verifies a recent hook for this session's current task
// and checkout. It does not infer activation from configuration or chat output.
func CodexActivationStatus(root, repo, session string) (string, string) {
	var receipt codexHookReceipt
	if err := Read(codexHookReceiptPath(root, repo, session), &receipt); err != nil {
		if os.IsNotExist(err) {
			return "pending", "No hook receipt for this repository and session."
		}
		return "error", "Cannot read hook receipt: " + err.Error()
	}
	var turn Turn
	if err := Read(TurnPath(root, session), &turn); err != nil {
		return "error", "Cannot read current hook task: " + err.Error()
	}
	age := time.Since(receipt.At)
	if receipt.Repository != repo || receipt.Owner.Session != session || receipt.Owner.Turn == "" ||
		receipt.Owner != turn.Owner || turn.Agent != "codex" || receipt.At.IsZero() || age < 0 || age > 5*time.Minute {
		return "pending", "No recent hook receipt for the current task."
	}
	if receipt.Status != "processed" {
		return "error", receipt.Status
	}
	if strings.TrimSpace(turn.Prompt) == "" {
		return "error", "The current task prompt was not captured by a hook."
	}
	return "ready", "A hook processed the current task in this repository."
}
