package livewatch

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

// HypothesisRequest is bound to one immutable capture, never the next edit.
type HypothesisRequest struct {
	ID         string `json:"request_id"`
	Repository string `json:"repository"`
	Diff       string `json:"diff"`
	Partial    bool   `json:"partial,omitempty"`
}
type HypothesisResponse struct {
	RequestID  string                              `json:"request_id"`
	Hypotheses []adversarylabs.WorkspaceHypothesis `json:"hypotheses"`
}
type hypothesisWait struct {
	request HypothesisRequest
	reply   chan []adversarylabs.WorkspaceHypothesis
}

func SubmitHypotheses(ctx context.Context, key string, response HypothesisResponse) (Response, error) {
	return call(ctx, key, request{Key: key, Operation: "hypotheses", Hypotheses: &response})
}
func (w *worker) recordHypotheses(response HypothesisResponse) error {
	pending := w.hypotheses[response.RequestID]
	if pending == nil {
		return fmt.Errorf("unknown or expired hypothesis request")
	}
	if response.Hypotheses == nil || len(response.Hypotheses) > 5 {
		return fmt.Errorf("hypotheses must be an explicit array of at most five items")
	}
	for _, h := range response.Hypotheses {
		if !validHypothesis(h) {
			return fmt.Errorf("invalid hypothesis; path, positive line, concern and check are required")
		}
	}
	pending.reply <- response.Hypotheses
	delete(w.hypotheses, response.RequestID)
	return nil
}
func validHypothesis(h adversarylabs.WorkspaceHypothesis) bool {
	return h.Path != "" && !strings.HasPrefix(h.Path, "/") && path.Clean(h.Path) == h.Path && h.Path != ".." && !strings.HasPrefix(h.Path, "../") && h.Line > 0 && strings.TrimSpace(h.Hypothesis) != "" && strings.TrimSpace(h.Check) != "" && len([]rune(h.Hypothesis)) <= 400 && len([]rune(h.Check)) <= 400
}
