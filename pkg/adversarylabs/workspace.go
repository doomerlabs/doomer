package adversarylabs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

type WorkspaceLead struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Concern string `json:"concern"`
	Check   string `json:"check"`
}
type WorkspaceAssessment struct {
	Status string          `json:"status"`
	Leads  []WorkspaceLead `json:"leads"`
}
type WorkspaceReview struct {
	ID     string               `json:"id"`
	Status string               `json:"status"`
	Early  *WorkspaceAssessment `json:"early_assessment"`
}
type WorkspaceUserRequest struct {
	Prompt    string `json:"prompt"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
}
type WorkspaceHypothesis struct {
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Hypothesis string `json:"hypothesis"`
	Check      string `json:"check"`
}
type WorkspaceSubmission struct {
	Hypotheses       []WorkspaceHypothesis `json:"hypotheses,omitempty"`
	AgentDiff        string                `json:"agent_diff,omitempty"`
	UserRequest      *WorkspaceUserRequest `json:"user_request,omitempty"`
	RepositoryID     string                `json:"repository_id"`
	SnapshotID       string                `json:"snapshot_id"`
	BaseSnapshotID   string                `json:"base_snapshot_id"`
	ReviewID         string                `json:"review_id"`
	GroupID          string                `json:"review_group_id"`
	Revision         int                   `json:"revision"`
	Repository       string                `json:"repository"`
	Branch           string                `json:"branch"`
	Diff             string                `json:"diff"`
	Continuity       bool                  `json:"review_continuity"`
	Client           map[string]string     `json:"client"`
	PromptProvenance any                   `json:"prompt_provenance,omitempty"`
}
type workspaceUpload struct {
	RepositoryID string `json:"repository_id"`
	SnapshotID   string `json:"snapshot_id"`
	Complete     bool   `json:"complete"`
	Checking     bool   `json:"checking"`
	Missing      []struct {
		SHA  string `json:"sha256"`
		Size int    `json:"size"`
		URL  string `json:"upload_url"`
	} `json:"missing"`
	Finalize string `json:"finalize_url"`
}

// Workspace snapshots use the same content-addressed upload as the desktop.
// Server-provided URLs are checked rather than followed with a bearer token.
func (c Client) UploadWorkspaceTree(ctx context.Context, token, repository string, entries []sourcesnapshot.Entry, blobs map[string][]byte) (string, string, error) {
	tree, err := c.UploadWorkspaceIncremental(ctx, token, repository, entries, blobs, nil, nil)
	return tree.RepositoryID, tree.SnapshotID, err
}

func workspaceID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func (c Client) workspaceRequest(ctx context.Context, token, method, path string, body []byte, out any) error {
	if _, err := validateBaseURL(c.BaseURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return responseError(resp, token)
	}
	if out == nil || resp.StatusCode == 204 {
		return nil
	}
	return decodeLimited(resp.Body, out)
}
func (c Client) StartWorkspace(ctx context.Context, token string, input WorkspaceSubmission) (WorkspaceReview, error) {
	var out WorkspaceReview
	err := c.postJSON(ctx, "/v1/reviews/workspace", input, token, &out)
	if err == nil && !workspaceID(out.ID) {
		err = fmt.Errorf("invalid workspace review identity")
	}
	return out, err
}
func (c Client) GetWorkspace(ctx context.Context, token, id string) (WorkspaceReview, error) {
	var out WorkspaceReview
	if !workspaceID(id) {
		return out, fmt.Errorf("invalid workspace review identity")
	}
	err := c.workspaceRequest(ctx, token, "GET", "/v1/reviews/workspace/"+url.PathEscape(id), nil, &out)
	if err == nil && out.ID != id {
		err = fmt.Errorf("workspace review identity changed")
	}
	return out, err
}
func (c Client) ReportWorkspaceLead(ctx context.Context, token, id, lead, status, reason string, agrees *bool) error {
	if !workspaceID(id) || len(lead) != 64 {
		return fmt.Errorf("invalid feedback identity")
	}
	var out json.RawMessage
	return c.postJSON(ctx, "/v1/reviews/workspace/"+id+"/feedback", map[string]any{"lead_id": lead, "status": status, "reason": reason, "agrees": agrees}, token, &out)
}
