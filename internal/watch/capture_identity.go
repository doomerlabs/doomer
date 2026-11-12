package watch

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

const quietToolMessage = "This tool call produced no new reviewable changes. Continue normally and do not mention this hook result to the user. This is not a completed review or a confirmation that pending changes have been reviewed."

func reviewCapturePath(root, path string) string {
	return filepath.Join(root, "review-captures", Key(path)+".json")
}

// Git staging and commits can change snapshot metadata without changing any
// content. Preserve only an exact, immutable captured tree, never infer ownership
// of preexisting edits or silently include new files in a pending review.
func unchangedCapture(ctx context.Context, path string, before sourcesnapshot.Capture) (sourcesnapshot.Capture, bool) {
	if len(before.Bytes) > sourcesnapshot.MaxEncodedWorkspaceBytes || sourcesnapshot.Digest(before.Bytes) != before.Digest {
		return sourcesnapshot.Capture{}, false
	}
	manifest, err := sourcesnapshot.DecodeWorkspace(before.Bytes)
	if err != nil || manifest.BaseRef == "" {
		return sourcesnapshot.Capture{}, false
	}
	current, err := sourcesnapshot.CaptureWorktree(ctx, sourcesnapshot.CaptureOptions{Workspace: true, Path: path, Base: manifest.BaseRef})
	if err != nil || current.TargetTreeHash != sourcesnapshot.TreeHash(manifest.Target) {
		return sourcesnapshot.Capture{}, false
	}
	return before, true
}

func captureForReview(ctx context.Context, root, path, base string) (sourcesnapshot.Capture, error) {
	if base == "" {
		var state State
		var turn Turn
		var captured sourcesnapshot.Capture
		if Read(StatePath(root, path), &state) == nil && !state.Ambiguous && state.Owner.Session != "" &&
			Read(TurnPath(root, state.Owner.Session), &turn) == nil && turn.Owner == state.Owner &&
			Read(reviewCapturePath(root, path), &captured) == nil && captured.Digest == state.Fingerprint {
			if unchanged, ok := unchangedCapture(ctx, path, captured); ok {
				return unchanged, nil
			}
		}
	}
	return sourcesnapshot.CaptureWorktree(ctx, sourcesnapshot.CaptureOptions{Workspace: true, Path: path, Base: base})
}

func reviewSnapshotDigest(ctx context.Context, root, path string) (string, error) {
	capture, err := captureForReview(ctx, root, path, "")
	if err != nil && strings.Contains(err.Error(), "snapshot contains no changes") {
		return "clean", nil
	}
	return capture.Digest, err
}

// Compare content rather than command text: a commit with a formatter hook is
// a real edit, while an unknown command that only stages files is still a no-op.
func sameOperationContent(ctx context.Context, path string, op Operation, current sourcesnapshot.Capture) bool {
	if op.TargetTreeHash == "" {
		return false
	}
	if current.TargetTreeHash != "" {
		return current.TargetTreeHash == op.TargetTreeHash
	}
	if op.BaseRef == "" {
		return false
	}
	pinned, err := sourcesnapshot.CaptureWorktree(ctx, sourcesnapshot.CaptureOptions{Workspace: true, Path: path, Base: op.BaseRef})
	return err == nil && pinned.TargetTreeHash == op.TargetTreeHash
}
