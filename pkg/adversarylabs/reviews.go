package adversarylabs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

type ReviewSubmissionRequest struct {
	Project        string                        `json:"project"`
	IdempotencyKey string                        `json:"idempotencyKey"`
	Digest         string                        `json:"digest"`
	Source         sourcesnapshot.SourceMetadata `json:"source"`
}

type ReviewSubmission struct {
	ID              string                         `json:"id"`
	Status          string                         `json:"status"`
	Digest          string                         `json:"digest"`
	TreeHashVersion string                         `json:"treeHashVersion,omitempty"`
	BaseTreeHash    string                         `json:"baseTreeHash,omitempty"`
	TargetTreeHash  string                         `json:"targetTreeHash,omitempty"`
	Source          *sourcesnapshot.SourceMetadata `json:"source,omitempty"`
}

func (c Client) CreateReviewSubmission(ctx context.Context, token string, submission ReviewSubmissionRequest) (ReviewSubmission, error) {
	var out ReviewSubmission
	err := c.postJSON(ctx, "/v1/reviews/submissions", submission, token, &out)
	if err != nil {
		return out, err
	}
	if out.ID == "" || out.Digest != submission.Digest {
		return out, fmt.Errorf("invalid review submission response")
	}
	return out, nil
}

func (c Client) UploadReviewSnapshot(ctx context.Context, token, id string, snapshot []byte) error {
	if _, err := validateBaseURL(c.BaseURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+"/v1/reviews/submissions/"+url.PathEscape(id)+"/snapshot", bytes.NewReader(snapshot))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp, token)
	}
	return nil
}

func (c Client) FinalizeReviewSubmission(ctx context.Context, token, id string) (ReviewSubmission, error) {
	var out ReviewSubmission
	err := c.postJSON(ctx, "/v1/reviews/submissions/"+url.PathEscape(id)+"/finalize", struct{}{}, token, &out)
	return out, err
}

// ReviewResult preserves findings and source evidence returned by the snapshot API.
type ReviewResult struct {
	SnapshotDigest string            `json:"snapshotDigest,omitempty"`
	Conclusion     string            `json:"conclusion"`
	Summary        string            `json:"summary"`
	Findings       []json.RawMessage `json:"findings"`
	CoverageGaps   []string          `json:"coverageGaps,omitempty"`
	CodeContexts   json.RawMessage   `json:"codeContexts,omitempty"`
}

type ReviewStatus struct {
	ReviewSubmission
	Result *ReviewResult `json:"result,omitempty"`
	Error  string        `json:"error,omitempty"`
}

func (c Client) GetReview(ctx context.Context, token, id string) (ReviewStatus, error) {
	var out ReviewStatus
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, "/\\?#") || id == "." || id == ".." {
		return out, fmt.Errorf("invalid review ID")
	}
	if _, err := validateBaseURL(c.BaseURL); err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/reviews/submissions/"+url.PathEscape(id), nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, responseError(resp, token)
	}
	if err := decodeLimited(resp.Body, &out); err != nil {
		return out, err
	}
	if out.ID != id || out.Status == "" {
		return out, fmt.Errorf("invalid review status response")
	}
	return out, nil
}
