package watch

import (
	"context"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

const workspaceUploadTimeout = 15 * time.Minute

type baselineRequest struct {
	Owner       Owner
	RequestedAt time.Time
}

func baselineRequestPath(root, path string) string {
	return filepath.Join(root, "preuploads", Key(path)+".json")
}

// Called only by the trusted prompt hook after checking repository consent.
// The watcher does the network work; the prompt hook remains quick.
func requestBaseline(root, path string, owner Owner) {
	_ = Write(baselineRequestPath(root, path), baselineRequest{owner, time.Now()})
}

func (r Runner) prepareBaseline(ctx context.Context, path string, since time.Time, completed *Owner) {
	var request baselineRequest
	if Read(baselineRequestPath(r.Root, path), &request) != nil || request.Owner == *completed || request.Owner.Session == "" || request.Owner.Turn == "" || request.RequestedAt.Before(since) || !Allowed(r.Config, path) || RepositoryRemoved(r.Root, path) {
		return
	}
	var turn Turn
	if Read(TurnPath(r.Root, request.Owner.Session), &turn) != nil || turn.Owner != request.Owner {
		return
	}
	// A failed warm-up must not repeatedly hold up the real edit review. That
	// review retries the same content-addressed upload and reuses accepted blobs.
	*completed = request.Owner
	uploadCtx, cancel := context.WithTimeout(ctx, workspaceUploadTimeout)
	defer cancel()
	manifest, repo, err := sourcesnapshot.CaptureBaseline(uploadCtx, sourcesnapshot.CaptureOptions{Path: path, Base: r.Base})
	if err == nil {
		r.logf("%s: preparing the repository baseline before edits.", filepath.Base(path))
		err = r.uploadBaseline(uploadCtx, path, repo, manifest)
	}
	if err != nil && ctx.Err() == nil {
		r.logf("%s: baseline preparation did not finish; the edit review will retry it (%s).", filepath.Base(path), err)
	}
}
