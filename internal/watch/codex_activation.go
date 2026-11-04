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
func recordCodexHook(root, repo string, owner Owner, status string) error {
	return Write(codexHookReceiptPath(root, repo, owner.Session), codexHookReceipt{
		Repository: repo, Owner: owner, At: time.Now(), Status: status,
	})
}

// CodexActivationStatus verifies a recent hook for this session's current task
// and checkout. It does not infer activation from configuration or chat output.
func CodexActivationStatus(root, repo, session string) (string, string) {
	var receipt codexHookReceipt
	receiptErr := Read(codexHookReceiptPath(root, repo, session), &receipt)
	var turn Turn
	turnErr := Read(TurnPath(root, session), &turn)
	// Diagnostics live separately from receipts, so a failure creating or
	// replacing a receipt can still be reported by the read-only check.
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		var diagnostic hookDiagnostic
		if Read(filepath.Join(root, "hook-diagnostics", Key(session+event)+".json"), &diagnostic) == nil &&
			turnErr == nil && diagnostic.Owner == turn.Owner && turn.Agent == "codex" &&
			diagnostic.Repository == repo && diagnostic.At.After(receipt.At) &&
			strings.HasPrefix(diagnostic.Status, "activation receipt failed:") {
			age := time.Since(diagnostic.At)
			if age >= 0 && age < 5*time.Minute {
				return "error", diagnostic.Status
			}
		}
	}
	if receiptErr != nil {
		if os.IsNotExist(receiptErr) {
			return "pending", "No hook receipt for this repository and session."
		}
		return "error", "Cannot read hook receipt: " + receiptErr.Error()
	}
	if turnErr != nil {
		return "error", "Cannot read current hook task: " + turnErr.Error()
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
