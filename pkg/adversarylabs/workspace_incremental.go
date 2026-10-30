package adversarylabs

import (
	"context"
	"fmt"
	"strings"
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
	full := map[string]any{"repository": repository, "files": files, "verification_limit": 256, "protocol": "packs-v1"}
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
		body = map[string]any{"repository_id": previous.RepositoryID, "base_snapshot_id": previous.SnapshotID, "upserts": upserts, "deletes": deletes, "verification_limit": 256, "protocol": "packs-v1"}
	}
	var out workspaceUpload
	stage := time.Now()
	report("manifest_start", changed, 0, stage)
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
		}
		if len(out.Missing) > 0 && out.PackURL != "/api/v1/repositories/packs" {
			return WorkspaceTree{}, fmt.Errorf("server does not support pack uploads; update the SaaS before retrying")
		}
		if out.ProofURL == "/api/v1/repositories/proofs" && len(out.Missing) > 0 {
			proofStarted := time.Now()
			report("proof_start", len(out.Missing), 0, proofStarted)
			reused, proofErr := c.proveWorkspaceBlobs(ctx, token, out.Missing, blobs)
			if proofErr != nil {
				return WorkspaceTree{}, fmt.Errorf("content verification failed: %w", proofErr)
			}
			if err := ctx.Err(); err != nil {
				return WorkspaceTree{}, err
			}
			remaining := make([]workspaceMissing, 0, len(out.Missing))
			saved := 0
			for _, file := range out.Missing {
				if reused[file.SHA] {
					saved += file.Size
				} else {
					remaining = append(remaining, file)
				}
			}
			report("proofs", len(out.Missing)-len(remaining), saved, proofStarted)
			out.Missing = remaining
		}
		if len(out.Missing) > 0 {
			if out.PackURL != "/api/v1/repositories/packs" {
				return WorkspaceTree{}, fmt.Errorf("server does not support pack uploads; update the SaaS before retrying")
			}
			err := visitWorkspacePacks(out.Missing, blobs, func(pack workspacePack) error {
				sent, err := c.uploadWorkspacePack(ctx, token, pack, progress)
				if err != nil {
					return err
				}
				count += len(pack.Entries)
				total += sent
				return nil
			})
			if err != nil {
				return WorkspaceTree{}, err
			}
		}
		expected := "/api/v1/repositories/" + repo + "/snapshots/" + id + "/finalize"
		if out.Finalize != "" && out.Finalize != expected {
			return WorkspaceTree{}, fmt.Errorf("unexpected finalize URL")
		}
		stage = time.Now()
		report("finalize_start", 0, 0, stage)
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
