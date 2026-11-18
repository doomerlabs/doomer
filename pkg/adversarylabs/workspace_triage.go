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
const maxTriageBytes = 4 * 1024 * 1024

type triagePart struct {
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// Large diffs travel as checksummed, owner-scoped parts. The final request is
// the only one which runs classification; no partial upload is a clean review.
func (c Client) TriageWorkspace(ctx context.Context, token, reviewID, diff string) ([]WorkspaceReviewArea, error) {
	if len(diff) > maxTriageBytes {
		return nil, &ResponseError{StatusCode: 413, Code: "triage_diff_too_large", message: "triage_diff_too_large: diff exceeds the 4 MiB Quick review budget; split the change"}
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	hash := sourcesnapshot.Digest([]byte(diff))
	var result struct {
		Areas    []WorkspaceReviewArea `json:"areas"`
		Complete bool                  `json:"complete"`
		DiffHash string                `json:"diff_hash"`
	}
	encoded, err := json.Marshal(map[string]string{"review_id": reviewID, "diff": diff})
	if err != nil {
		return nil, err
	}
	if len(diff) <= 64*1024 && len(encoded) <= 150*1024 {
		err := c.postJSON(ctx, "/v1/reviews/workspace/triage", map[string]string{"review_id": reviewID, "diff": diff}, token, &result)
		return result.Areas, err
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
			return nil, fmt.Errorf("upload triage part %d: %w", len(parts)+1, err)
		}
		if ack != part {
			return nil, fmt.Errorf("triage part acknowledgement mismatch")
		}
		parts = append(parts, part)
	}
	err = c.postJSON(ctx, "/v1/reviews/workspace/triage", map[string]any{"action": "complete", "review_id": reviewID, "diff_hash": hash, "parts": parts}, token, &result)
	if err != nil {
		return nil, err
	}
	if !result.Complete || !strings.EqualFold(result.DiffHash, hash) {
		return nil, fmt.Errorf("triage incomplete: final diff acknowledgement mismatch")
	}
	return result.Areas, nil
}
