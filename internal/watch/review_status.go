package watch

import (
	"context"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func workspaceFullStatus(review adversarylabs.WorkspaceReview) string {
	if review.Full == nil {
		return "unknown"
	}
	if !review.Full.Enabled {
		return "disabled"
	}
	return review.Full.Status
}

// An unavailable quick assessment can coexist with a running deeper review.
// Refresh the same submission without uploading it again or waiting for completion.
func (r Runner) refreshUnavailableReview(ctx context.Context, path string) {
	var feed Feed
	if Read(FeedPath(r.Root, path), &feed) != nil || feed.AssessmentStatus != "unavailable" || feed.ReviewID == "" || time.Since(feed.StatusCheckedAt) < 15*time.Second {
		return
	}
	switch feed.FullReviewStatus {
	case "completed", "failed", "cancelled", "disabled", "unknown":
		return
	}
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	current, err := r.Client.GetWorkspace(requestCtx, r.Token, feed.ReviewID)
	// Back off unsuccessful reads too, retaining the last known result.
	_ = Locked(r.Root, path, func() error {
		var latest Feed
		if Read(FeedPath(r.Root, path), &latest) != nil || latest.ReviewID != feed.ReviewID || latest.Owner != feed.Owner || latest.Fingerprint != feed.Fingerprint {
			return nil
		}
		latest.StatusCheckedAt = time.Now()
		if err == nil && current.ID == feed.ReviewID {
			latest.FullReviewStatus = workspaceFullStatus(current)
			latest.Updated = time.Now()
		}
		return Write(FeedPath(r.Root, path), latest)
	})
}
