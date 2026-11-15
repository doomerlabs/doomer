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
	Areas      []adversarylabs.WorkspaceReviewArea `json:"areas,omitempty"`
	ID         string                              `json:"request_id"`
	ReviewID   string                              `json:"review_id"`
	Revision   int                                 `json:"revision"`
	Snapshot   string                              `json:"snapshot_id"`
	DiffHash   string                              `json:"diff_hash"`
	Repository string                              `json:"repository"`
	Diff       string                              `json:"diff"`
	Partial    bool                                `json:"partial,omitempty"`
}
type AreaAssessment struct {
	AreaID   string `json:"area_id"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}
type HypothesisResponse struct {
	Assessments []AreaAssessment                    `json:"assessments,omitempty"`
	Status      string                              `json:"status,omitempty"`
	Reason      string                              `json:"reason,omitempty"`
	RequestID   string                              `json:"request_id"`
	Hypotheses  []adversarylabs.WorkspaceHypothesis `json:"hypotheses"`
}
type hypothesisWait struct {
	request HypothesisRequest
	reply   chan HypothesisResponse
}

func SubmitHypotheses(ctx context.Context, key string, response HypothesisResponse) (Response, error) {
	return call(ctx, key, request{Key: key, Operation: "hypotheses", Hypotheses: &response})
}
func (w *worker) recordHypotheses(response HypothesisResponse) error {
	pending := w.hypotheses[response.RequestID]
	if pending == nil {
		return fmt.Errorf("unknown or expired hypothesis request")
	}
	if response.Status == "" {
		response.Status = "assessed"
	}
	if response.Status != "assessed" && response.Status != "superseded" && response.Status != "incomplete" {
		return fmt.Errorf("invalid assessment status")
	}
	if response.Status != "assessed" && (strings.TrimSpace(response.Reason) == "" || len(response.Hypotheses) > 0) {
		return fmt.Errorf("unassessed snapshots require a reason and no hypotheses")
	}
	if response.Hypotheses == nil || len(response.Hypotheses) > 5 {
		return fmt.Errorf("hypotheses must be an explicit array of at most five items")
	}
	if response.Status == "assessed" && len(pending.request.Areas) > 0 {
		seen := map[string]bool{}
		for _, a := range response.Assessments {
			known := false
			for _, area := range pending.request.Areas {
				if area.ID == a.AreaID {
					known = true
				}
			}
			if !known || seen[a.AreaID] || strings.TrimSpace(a.Evidence) == "" || len([]rune(a.Evidence)) > 1200 || (a.Status != "concern" && a.Status != "no_issue") {
				return fmt.Errorf("each selected area requires one concern or no_issue assessment with evidence")
			}
			seen[a.AreaID] = true
			if a.Status == "concern" && len(response.Hypotheses) == 0 {
				return fmt.Errorf("concern assessment requires a hypothesis")
			}
		}
		if len(seen) != len(pending.request.Areas) {
			return fmt.Errorf("assessment required for every selected area")
		}
	}
	for _, h := range response.Hypotheses {
		if !validHypothesis(h) {
			return fmt.Errorf("invalid hypothesis; path, positive line, concern and check are required")
		}
	}
	pending.reply <- response
	delete(w.hypotheses, response.RequestID)
	return nil
}
func validHypothesis(h adversarylabs.WorkspaceHypothesis) bool {
	return h.Path != "" && !strings.HasPrefix(h.Path, "/") && path.Clean(h.Path) == h.Path && h.Path != ".." && !strings.HasPrefix(h.Path, "../") && h.Line > 0 && strings.TrimSpace(h.Hypothesis) != "" && strings.TrimSpace(h.Check) != "" && len([]rune(h.Hypothesis)) <= 400 && len([]rune(h.Check)) <= 400
}
