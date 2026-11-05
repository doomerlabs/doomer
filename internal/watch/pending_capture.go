package watch

import (
	"context"
	"errors"
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

func resumeCompletedEdit(ctx context.Context, root, path string, capture sourcesnapshot.Capture, since time.Time) (bool, error) {
	resumed := false
	var failure error
	err := Locked(root, path, func() error {
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
				agentDiff, diffErr := capturedAgentDiff(ctx, root, path, state, op.Owner, id, op.Before, capture)
				if diffErr != nil {
					if ctx.Err() != nil {
						return nil
					}
					op.CaptureAttempts++
					if errors.Is(diffErr, errScopeDiff) && op.CaptureAttempts < 3 {
						state.Active[id] = op
						return Write(StatePath(root, path), state)
					}
					// Persist a failed outcome owned by this edit, without a new
					// review request. Removing the operation makes this terminal.
					state.Owner = op.Owner
					state.Fingerprint = capture.Digest
					state.RequestedAt = time.Time{}
					state.AgentDiff = ""
					feed := Feed{Owner: op.Owner, Fingerprint: capture.Digest, Updated: time.Now(), AssessmentStatus: "unavailable", FailureCode: "capture_edit_scope"}
					if err := Write(FeedPath(root, path), feed); err != nil {
						return err
					}
					if err := Write(StatePath(root, path), state); err != nil {
						return err
					}
					failure = diffErr
					recordHookDiagnostic(root, op.Owner.Session, hookDiagnostic{At: time.Now(), Event: "CaptureRecovery", Repository: path, ToolID: id, HasToolID: true, Owner: op.Owner, Status: diffErr.Error()})
					return nil
				}
				if agentDiff == "" {
					return Write(StatePath(root, path), state)
				}
				if err := Write(reviewCapturePath(root, path), capture); err != nil {
					return err
				}
				state.Owner = op.Owner
				state.Fingerprint = capture.Digest
				state.RequestedAt = op.CompletedAt
				state.InlineWaited = false
				state.Prompt = turn.Prompt
				state.Agent = turn.Agent
				state.QuestionRequest = ""
				state.HypothesesSubmitted = false
				state.Hypotheses = nil
				state.AgentDiff = agentDiff
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
	return resumed, errors.Join(failure, err)
}
