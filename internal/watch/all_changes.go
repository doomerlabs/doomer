package watch

import (
	"context"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

// A prompt gives changes task context; ownership here never claims which tool
// or person edited the source. Upload permission still comes from Config.
func allChangesTask(root, path string) (baselineRequest, Turn, bool) {
	var request baselineRequest
	var turn Turn
	if Read(baselineRequestPath(root, path), &request) != nil || request.Owner.Session == "" || request.Owner.Turn == "" ||
		Read(TurnPath(root, request.Owner.Session), &turn) != nil || turn.Owner != request.Owner {
		return request, turn, false
	}
	return request, turn, true
}

func (r Runner) queueAllChanges(ctx context.Context, path string, capture sourcesnapshot.Capture) (bool, error) {
	if !r.Config.AllChanges || !Allowed(r.Config, path) || RepositoryRemoved(r.Root, path) {
		return false, nil
	}
	request, turn, active := allChangesTask(r.Root, path)
	if !active {
		return false, nil
	}
	manifest, err := sourcesnapshot.DecodeWorkspace(capture.Bytes)
	if err != nil {
		return false, err
	}
	patch, err := Diff(ctx, manifest)
	if err != nil || strings.TrimSpace(patch) == "" {
		return false, err
	}
	queued := false
	err = Locked(r.Root, path, func() error {
		// Do not attach a slow capture to a newer prompt.
		current, _, ok := allChangesTask(r.Root, path)
		if !ok || current.Owner != request.Owner {
			return nil
		}
		var previous State
		if Read(StatePath(r.Root, path), &previous) == nil && previous.Owner == request.Owner && previous.Fingerprint == capture.Digest && !previous.RequestedAt.IsZero() && !previous.Ambiguous && previous.Agent == "filesystem" && strings.TrimSpace(previous.AgentDiff) != "" {
			queued = true
			return nil
		}
		if err := Write(reviewCapturePath(r.Root, path), capture); err != nil {
			return err
		}
		state := State{Owner: request.Owner, Fingerprint: capture.Digest, RequestedAt: time.Now(), AgentDiff: patch,
			Agent: "filesystem", Prompt: turn.Prompt, HypothesesSubmitted: true, Active: map[string]Operation{}}
		if err := Write(StatePath(r.Root, path), state); err != nil {
			return err
		}
		queued = true
		return nil
	})
	if err != nil || !queued {
		return queued, err
	}
	err = Locked(r.Root, request.Owner.Session, func() error {
		var latest Turn
		if Read(TurnPath(r.Root, request.Owner.Session), &latest) != nil || latest.Owner != request.Owner {
			return nil
		}
		for _, existing := range latest.Repositories {
			if existing == path {
				return nil
			}
		}
		latest.Repositories = append(latest.Repositories, path)
		return Write(TurnPath(r.Root, request.Owner.Session), latest)
	})
	return queued, err
}
