package watch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
)

// Attribution updates retry the shared session lock briefly instead of dropping
// concurrent callbacks. Cancellation and the hook budget bound the wait.
func coverageLocked(ctx context.Context, root, session string, update func() error) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		lock, err := publock.TryAcquire(root, session)
		if err == nil {
			defer lock.Close()
			return update()
		}
		if !publock.Busy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Keep unidentified writes separate from repository review state. A later
// capture in another checkout cannot establish coverage for these operations.
func recordUnresolvedOperation(ctx context.Context, root, turnFile string, owner Owner, input HookInput) string {
	message := ""
	id := input.ToolID
	if id == "" {
		// Missing IDs cannot be correlated with a later successful invocation.
		id = "unidentified-" + Key(input.Tool+"\n"+input.CWD+"\n"+string(input.Input))
	}
	_ = coverageLocked(ctx, root, owner.Session, func() error {
		var turn Turn
		if Read(turnFile, &turn) != nil || turn.Owner != owner {
			return nil
		}
		for _, operation := range turn.UnresolvedOperations {
			if operation.ToolID == id {
				return nil
			}
		}
		turn.UnresolvedOperations = append(turn.UnresolvedOperations, UnresolvedOperation{
			ToolID: id, Tool: input.Tool, Directory: input.CWD, Completed: input.ToolID == "",
		})
		if err := Write(turnFile, turn); err != nil {
			return err
		}
		message = fmt.Sprintf("Doomer could not attribute tool %q (%s) from %q because its repository could not be identified. Set the tool's working directory to the target checkout or use explicit file paths before editing. This operation has unverified review coverage. Later reviews cover only their captured changes. Continue the task without repeating setup.", id, input.Tool, input.CWD)
		return nil
	})
	return message
}

// A corrected PreToolUse can recover the same invocation only before it has
// completed. PostToolUse alone cannot reconstruct the missing pre-edit baseline.
func reconcileUnresolvedOperation(ctx context.Context, root, turnFile string, owner Owner, input HookInput) string {
	if input.ToolID == "" {
		return ""
	}
	message := ""
	_ = coverageLocked(ctx, root, owner.Session, func() error {
		var turn Turn
		if Read(turnFile, &turn) != nil || turn.Owner != owner {
			return nil
		}
		for i, operation := range turn.UnresolvedOperations {
			if operation.ToolID != input.ToolID {
				continue
			}
			if input.Event == "PostToolUse" {
				turn.UnresolvedOperations[i].Completed = true
			} else if input.Event == "PreToolUse" && !operation.Completed {
				turn.UnresolvedOperations = append(turn.UnresolvedOperations[:i], turn.UnresolvedOperations[i+1:]...)
			} else {
				return nil
			}
			if err := Write(turnFile, turn); err != nil {
				return err
			}
			if input.Event == "PreToolUse" {
				message = fmt.Sprintf("Doomer recovered repository attribution and the pre-edit capture for tool %q. Its earlier repository diagnostic is resolved. Subsequent capture and review results determine coverage.", input.ToolID)
			}
			return nil
		}
		return nil
	})
	return message
}

func unresolvedCoverageNotice(ctx context.Context, root, turnFile string, owner Owner) string {
	message := ""
	_ = coverageLocked(ctx, root, owner.Session, func() error {
		var turn Turn
		if Read(turnFile, &turn) != nil || turn.Owner != owner || len(turn.UnresolvedOperations) == 0 {
			return nil
		}
		operations := make([]string, 0, len(turn.UnresolvedOperations))
		for _, operation := range turn.UnresolvedOperations {
			operations = append(operations, fmt.Sprintf("tool %q (%s) from %q", operation.ToolID, operation.Tool, operation.Directory))
		}
		detail := strings.Join(operations, "; ")
		key := "unresolved-coverage:" + Key(detail)
		for _, delivered := range turn.Delivered {
			if delivered == key {
				return nil
			}
		}
		turn.Delivered = append(turn.Delivered, key)
		message = "Doomer could not identify the repository for these potentially mutating operations: " + detail + ". Their review coverage remains unverified. Report this scoped gap separately from any completed reviews. A later review does not establish coverage for an unidentified operation. Do not describe all repository reviews as failed or repeat setup."
		if turn.ReviewContext != "" {
			turn.ReviewContinuation = strings.TrimSpace(turn.ReviewContinuation + "\n\n" + message)
		}
		if err := Write(turnFile, turn); err != nil {
			message = ""
			return err
		}
		return nil
	})
	return message
}
