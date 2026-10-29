package watch

import (
	"context"
	"errors"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"path/filepath"
	"time"
)

var errRepositoryMismatch = errors.New("base and current repository do not match")

type incrementalUploader interface {
	UploadWorkspaceIncremental(context.Context, string, string, []sourcesnapshot.Entry, map[string][]byte, *adversarylabs.WorkspaceTree, adversarylabs.WorkspaceUploadProgress) (adversarylabs.WorkspaceTree, error)
}
type uploadCache struct{ Base, Head adversarylabs.WorkspaceTree }

func (r Runner) uploadSnapshots(ctx context.Context, path, repository string, manifest sourcesnapshot.Manifest) (string, string, string, error) {
	uploader, ok := r.Client.(incrementalUploader)
	if !ok {
		repo, base, err := r.Client.UploadWorkspaceTree(ctx, r.Token, repository, manifest.Base, manifest.Blobs)
		if err != nil {
			return "", "", "", err
		}
		headRepo, head, err := r.Client.UploadWorkspaceTree(ctx, r.Token, repository, manifest.Target, manifest.Blobs)
		if err != nil {
			return "", "", "", err
		}
		if repo != headRepo {
			return "", "", "", errRepositoryMismatch
		}
		return repo, base, head, nil
	}
	// Isolate cached manifests by destination, profile, login, and checkout. A
	// token replacement must never inherit another account's remote snapshot IDs.
	cachePath := filepath.Join(r.Root, "uploads", Key(r.Config.API+"\x00"+r.Config.Profile+"\x00"+r.Token+"\x00"+path)+".json")
	var cache uploadCache
	_ = Read(cachePath, &cache)
	progress := func(label string) adversarylabs.WorkspaceUploadProgress {
		return func(stage string, files, bytes int, elapsed time.Duration) {
			r.debugf("%s: %s %s: %d files, %d bytes, %s", path, label, stage, files, bytes, elapsed.Round(time.Millisecond))
		}
	}
	base, err := uploader.UploadWorkspaceIncremental(ctx, r.Token, repository, manifest.Base, manifest.Blobs, &cache.Base, progress("base"))
	if err != nil {
		return "", "", "", err
	}
	prior := &cache.Head
	if prior.SnapshotID == "" {
		prior = &base
	}
	head, err := uploader.UploadWorkspaceIncremental(ctx, r.Token, repository, manifest.Target, manifest.Blobs, prior, progress("current"))
	if err != nil {
		return "", "", "", err
	}
	if base.RepositoryID != head.RepositoryID {
		return "", "", "", errRepositoryMismatch
	}
	if err := Write(cachePath, uploadCache{Base: base, Head: head}); err != nil {
		r.debugf("%s: upload cache could not be saved", path)
	}
	return base.RepositoryID, base.SnapshotID, head.SnapshotID, nil
}
