package watch

import (
	"os"
	"path/filepath"
	"time"
)

// PrunePendingPrompts removes expired, unassigned prompt text even when the
// originating agent was killed before its Stop hook could run.
func PrunePendingPrompts(root string, now time.Time) {
	entries, err := os.ReadDir(filepath.Join(root, "turns"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		clearPendingPrompt(root, filepath.Join(root, "turns", entry.Name()), now, false)
	}
}

func clearPendingPrompt(root, file string, now time.Time, stopped bool) {
	var turn Turn
	if Read(file, &turn) != nil || turn.PendingPromptUntil.IsZero() {
		return
	}
	_ = Locked(root, turn.Owner.Session, func() error {
		if Read(file, &turn) != nil || turn.PendingPromptUntil.IsZero() {
			return nil
		}
		if !stopped && now.Before(turn.PendingPromptUntil) {
			return nil
		}
		turn.Prompt = ""
		turn.PendingPromptUntil = time.Time{}
		return Write(file, turn)
	})
}
