package adversarylabs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

const triagePartBytes = 32 * 1024
const maxTriageBytes = 8 * 1024 * 1024

type triagePart struct {
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// WorkspaceTriage describes the areas and whether the server can reuse the
// saved diff during final submission.
type WorkspaceTriage struct {
	Areas         []WorkspaceReviewArea `json:"areas"`
	Complete      bool                  `json:"complete"`
	DiffHash      string                `json:"diff_hash"`
	DiffReference bool                  `json:"diff_reference"`
}

func (c Client) TriageWorkspace(ctx context.Context, token, reviewID, diff string) ([]WorkspaceReviewArea, error) {
	result, err := c.PrepareWorkspaceTriage(ctx, token, reviewID, diff)
	return result.Areas, err
}

// Large diffs travel as checksummed, owner-scoped parts. The final request is
// the only one which runs classification; no partial upload is a clean review.
func (c Client) PrepareWorkspaceTriage(ctx context.Context, token, reviewID, diff string) (WorkspaceTriage, error) {
	if len(diff) > maxTriageBytes {
		return WorkspaceTriage{}, &ResponseError{StatusCode: 413, Code: "triage_diff_too_large", message: "The latest-edit diff exceeds the 8 MiB Quick Review limit. Split the change into smaller reviews."}
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	hash := sourcesnapshot.Digest([]byte(diff))
	var result WorkspaceTriage
	encoded, err := json.Marshal(map[string]string{"review_id": reviewID, "diff": diff})
	if err != nil {
		return WorkspaceTriage{}, err
	}
	if len(diff) <= 64*1024 && len(encoded) <= 150*1024 {
		err := c.postJSON(ctx, "/v1/reviews/workspace/triage", map[string]string{"review_id": reviewID, "diff": diff}, token, &result)
		if err != nil {
			return WorkspaceTriage{}, err
		}
		if result.DiffReference && (!result.Complete || !strings.EqualFold(result.DiffHash, hash)) {
			return WorkspaceTriage{}, fmt.Errorf("triage incomplete: final diff acknowledgement mismatch")
		}
		return result, nil
	}
	parts := make([]triagePart, 0, (len(diff)+triagePartBytes-1)/triagePartBytes)
	for start := 0; start < len(diff); start += triagePartBytes {
		end := start + triagePartBytes
		if end > len(diff) {
			end = len(diff)
		}
		data := []byte(diff[start:end])
		part := triagePart{sourcesnapshot.Digest(data), len(data)}
		payload := map[string]any{"action": "part", "review_id": reviewID, "diff_hash": hash, "sha256": part.SHA256, "data": base64.StdEncoding.EncodeToString(data)}
		var ack triagePart
		err := c.postJSON(ctx, "/v1/reviews/workspace/triage", payload, token, &ack)
		// Content addressing makes a single transport/server-error retry idempotent.
		if err != nil && ctx.Err() == nil {
			var response *ResponseError
			if !errors.As(err, &response) || response.StatusCode >= 500 {
				err = c.postJSON(ctx, "/v1/reviews/workspace/triage", payload, token, &ack)
			}
		}
		if err != nil {
			return WorkspaceTriage{}, fmt.Errorf("upload triage part %d: %w", len(parts)+1, err)
		}
		if ack != part {
			return WorkspaceTriage{}, fmt.Errorf("triage part acknowledgement mismatch")
		}
		parts = append(parts, part)
	}
	err = c.postJSON(ctx, "/v1/reviews/workspace/triage", map[string]any{"action": "complete", "review_id": reviewID, "diff_hash": hash, "parts": parts}, token, &result)
	if err != nil {
		return WorkspaceTriage{}, err
	}
	if !result.Complete || !strings.EqualFold(result.DiffHash, hash) {
		return WorkspaceTriage{}, fmt.Errorf("triage incomplete: final diff acknowledgement mismatch")
	}
	return result, nil
}
