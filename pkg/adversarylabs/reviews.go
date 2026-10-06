package adversarylabs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

type ReviewStatus struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Digest string          `json:"digest"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

func (c Client) CreateReview(ctx context.Context, token, project, key, digest string) (ReviewStatus, error) {
	var out ReviewStatus
	err := c.postJSON(ctx, "/v1/reviews/submissions", map[string]string{"project": project, "idempotencyKey": key, "digest": digest}, token, &out)
	return out, err
}
func (c Client) UploadReview(ctx context.Context, token, id string, data []byte) error {
	return c.reviewRequest(ctx, http.MethodPut, "/v1/reviews/submissions/"+url.PathEscape(id)+"/snapshot", token, data, nil)
}
func (c Client) FinalizeReview(ctx context.Context, token, id string) (ReviewStatus, error) {
	var out ReviewStatus
	err := c.postJSON(ctx, "/v1/reviews/submissions/"+url.PathEscape(id)+"/finalize", struct{}{}, token, &out)
	return out, err
}
func (c Client) Review(ctx context.Context, token, id string) (ReviewStatus, error) {
	var out ReviewStatus
	err := c.reviewRequest(ctx, http.MethodGet, "/v1/reviews/submissions/"+url.PathEscape(id), token, nil, &out)
	return out, err
}
func (c Client) reviewRequest(ctx context.Context, method, path, token string, data []byte, out any) error {
	if err := ValidateAPIURL(c.BaseURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp, token)
	}
	if out != nil {
		data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
		if err != nil {
			return err
		}
		if len(data) > 8<<20 {
			return fmt.Errorf("review response exceeds 8 MiB limit")
		}
		return json.Unmarshal(data, out)
	}
	return nil
}
func (r ReviewStatus) Validate() error {
	if r.ID == "" || r.Status == "" {
		return fmt.Errorf("invalid review response")
	}
	return nil
}
