package watch

import (
	"context"
	"os"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

// Keep the recorded pre-edit ownership when the post-edit capture races another
// save or times out. The watcher can complete it without another agent event.
func deferCompletedEdit(root, path string, owner Owner, toolID string) {
	_ = Locked(root, path, func() error {
		var state State
		if Read(StatePath(root, path), &state) != nil {
			return nil
		}
		op, ok := state.Active[toolID]
		if !ok || op.Owner != owner {
			return nil
		}
		if !op.Attributable {
			delete(state.Active, toolID)
			return Write(StatePath(root, path), state)
		}
		op.CompletedAt = time.Now()
		state.Active[toolID] = op
		return Write(StatePath(root, path), state)
	})
}

func resumeCompletedEdit(ctx context.Context, root, path string, capture sourcesnapshot.Capture, since time.Time) bool {
	resumed := false
	_ = Locked(root, path, func() error {
		var state State
		if Read(StatePath(root, path), &state) != nil || state.Ambiguous || len(state.Active) != 1 {
			return nil
		}
		for id, op := range state.Active {
			if !op.Attributable || op.CompletedAt.IsZero() || op.CompletedAt.Before(since) || time.Since(op.Started) > 30*time.Minute {
				return nil
			}
			var turn Turn
			if Read(TurnPath(root, op.Owner.Session), &turn) != nil || turn.Owner != op.Owner {
				return nil
			}
			if state.Owner.Session != "" && state.Owner.Session != op.Owner.Session {
				return nil
			}
			delete(state.Active, id)
			if capture.Digest != op.Before && !state.RequestedAt.After(op.CompletedAt) {
				state.Owner = op.Owner
				state.Fingerprint = capture.Digest
				state.RequestedAt = op.CompletedAt
				state.InlineWaited = false
				state.Prompt = turn.Prompt
				state.Agent = turn.Agent
				state.QuestionRequest = ""
				state.HypothesesSubmitted = false
				state.Hypotheses = nil
				state.AgentDiff = recentAgentDiff(ctx, operationSnapshotPath(root, path, op.Owner, id), op.Before, capture)
				resumed = true
			}
			if err := Write(StatePath(root, path), state); err != nil {
				resumed = false
				return err
			}
			_ = os.Remove(operationSnapshotPath(root, path, op.Owner, id))
		}
		return nil
	})
	return resumed
}
