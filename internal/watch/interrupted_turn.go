package watch

import (
	"errors"
	"os"
)

// A new user prompt supersedes unfinished operations from earlier turns of
// that same session. Interrupted tools may never send PostToolUse or Stop.
// Retire their bookkeeping, not their processes, and require a fresh edit
// before requesting another review. Other sessions still own their operations.
func retireInterruptedTurn(root, path string, owner Owner) error {
	return Locked(root, path, func() error {
		var state State
		if err := Read(StatePath(root, path), &state); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		var snapshots []string
		for id, op := range state.Active {
			if op.Owner.Session == owner.Session && op.Owner.Turn != owner.Turn {
				delete(state.Active, id)
				snapshots = append(snapshots, operationSnapshotPath(root, path, op.Owner, id))
			}
		}
		staleAmbiguity := state.Ambiguous && state.Owner.Session == owner.Session && state.Owner != owner
		if len(snapshots) == 0 && !staleAmbiguity {
			return nil
		}
		if len(state.Active) == 0 && (state.Owner.Session == "" || state.Owner.Session == owner.Session) {
			// The next PreToolUse establishes a baseline. Never attribute existing
			// dirty files or a partially completed old command to the new prompt.
			state = State{}
		}
		if err := Write(StatePath(root, path), state); err != nil {
			return err
		}
		for _, file := range snapshots {
			_ = os.Remove(file)
		}
		return nil
	})
}
