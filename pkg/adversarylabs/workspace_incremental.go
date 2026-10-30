package adversarylabs

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

type WorkspaceTree struct {
	RepositoryID string
	SnapshotID   string
	Entries      []sourcesnapshot.Entry
}
type WorkspaceUploadProgress func(stage string, files, bytes int, elapsed time.Duration)

// Manifests contain only changed metadata after the first upload. The server
// verifies the prior immutable snapshot, and requests only missing content.
func (c Client) UploadWorkspaceIncremental(ctx context.Context, token, repository string, entries []sourcesnapshot.Entry, blobs map[string][]byte, previous *WorkspaceTree, progress WorkspaceUploadProgress) (WorkspaceTree, error) {
	started := time.Now()
	report := func(stage string, files, bytes int, t time.Time) {
		if progress != nil {
			progress(stage, files, bytes, time.Since(t))
		}
	}
	file := func(e sourcesnapshot.Entry) map[string]any {
		return map[string]any{"path": e.Path, "sha256": e.Hash, "size": len(blobs[e.Hash]), "mode": e.Mode}
	}
	files := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		files = append(files, file(e))
	}
	full := map[string]any{"repository": repository, "files": files, "verification_limit": 256}
	body := full
	changed := len(entries)
	if previous != nil && workspaceID(previous.RepositoryID) && workspaceID(previous.SnapshotID) {
		old := map[string]sourcesnapshot.Entry{}
		for _, e := range previous.Entries {
			old[e.Path] = e
		}
		upserts := []map[string]any{}
		for _, e := range entries {
			prior, ok := old[e.Path]
			if !ok || prior.Hash != e.Hash || prior.Mode != e.Mode {
				upserts = append(upserts, file(e))
			}
			delete(old, e.Path)
		}
		deletes := []string{}
		for path := range old {
			deletes = append(deletes, path)
		}
		changed = len(upserts)
		body = map[string]any{"repository_id": previous.RepositoryID, "base_snapshot_id": previous.SnapshotID, "upserts": upserts, "deletes": deletes, "verification_limit": 256}
	}
	var out workspaceUpload
	stage := time.Now()
	err := c.postJSON(ctx, "/v1/repositories", body, token, &out)
	// Expired/deleted remote snapshots invalidate only this optimization. Never
	// retry authentication failures or other server failures as a full upload.
	if err != nil && previous != nil && strings.HasPrefix(err.Error(), "request failed: 410 ") {
		changed = len(entries)
		out = workspaceUpload{}
		err = c.postJSON(ctx, "/v1/repositories", full, token, &out)
	}
	report("manifest", changed, 0, stage)
	if err != nil {
		return WorkspaceTree{}, err
	}
	count, total := 0, 0
	for round := 0; round < 2*((len(entries)+255)/256)+10; round++ {
		if !workspaceID(out.RepositoryID) || !workspaceID(out.SnapshotID) {
			return WorkspaceTree{}, fmt.Errorf("invalid workspace upload identity")
		}
		repo, id := out.RepositoryID, out.SnapshotID
		if previous != nil && previous.RepositoryID != "" && repo != previous.RepositoryID {
			return WorkspaceTree{}, fmt.Errorf("workspace repository changed")
		}
		if out.Complete {
			if len(out.Missing) != 0 {
				return WorkspaceTree{}, fmt.Errorf("inconsistent upload")
			}
			report("complete", count, total, started)
			return WorkspaceTree{RepositoryID: repo, SnapshotID: id, Entries: entries}, nil
		}
		if len(out.Missing) == 0 && !out.Checking {
			return WorkspaceTree{}, fmt.Errorf("incomplete upload without missing blobs")
		}
		for _, missing := range out.Missing {
			data, ok := blobs[missing.SHA]
			if !ok || len(data) != missing.Size || missing.URL != "/api/v1/repositories/blobs/"+missing.SHA {
				return WorkspaceTree{}, fmt.Errorf("unexpected upload request")
			}
			count++
			total += len(data)
		}
		stage = time.Now()
		uploadCtx, cancel := context.WithCancel(ctx)
		jobs := make(chan int)
		failed := make(chan error, 1)
		var wg sync.WaitGroup
		for worker := 0; worker < min(4, len(out.Missing)); worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range jobs {
					missing := out.Missing[i]
					var ignored any
					if err := c.workspaceRequest(uploadCtx, token, "PUT", "/v1/repositories/blobs/"+missing.SHA, blobs[missing.SHA], &ignored); err != nil {
						select {
						case failed <- err:
						default:
						}
						cancel()
						return
					}
				}
			}()
		}
	send:
		for i := range out.Missing {
			select {
			case jobs <- i:
			case <-uploadCtx.Done():
				break send
			}
		}
		close(jobs)
		wg.Wait()
		cancel()
		select {
		case err := <-failed:
			return WorkspaceTree{}, err
		default:
		}
		if err := ctx.Err(); err != nil {
			return WorkspaceTree{}, err
		}
		report("blobs", len(out.Missing), total, stage)
		expected := "/api/v1/repositories/" + repo + "/snapshots/" + id + "/finalize"
		if out.Finalize != "" && out.Finalize != expected {
			return WorkspaceTree{}, fmt.Errorf("unexpected finalize URL")
		}
		stage = time.Now()
		out = workspaceUpload{}
		err = c.postJSON(ctx, strings.TrimPrefix(expected, "/api")+"?verification_limit=256", struct{}{}, token, &out)
		report("finalize", 0, 0, stage)
		if err != nil {
			return WorkspaceTree{}, err
		}
		if out.RepositoryID != repo || out.SnapshotID != id {
			return WorkspaceTree{}, fmt.Errorf("upload identity changed")
		}
	}
	return WorkspaceTree{}, fmt.Errorf("workspace upload did not complete")
}
