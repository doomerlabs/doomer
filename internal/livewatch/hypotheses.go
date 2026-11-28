package livewatch

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

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
	Assessments     []AreaAssessment                    `json:"assessments,omitempty"`
	Status          string                              `json:"status,omitempty"`
	Reason          string                              `json:"reason,omitempty"`
	RequestID       string                              `json:"request_id"`
	Hypotheses      []adversarylabs.WorkspaceHypothesis `json:"hypotheses"`
	triageReference bool
	denial          AssessmentDenial
}

type AssessmentDenial string

const (
	assessmentStatusDenied                           = "denied"
	AssessmentDenied                AssessmentDenial = "permission_denied"
	AssessmentOversightDenied       AssessmentDenial = "undermining_oversight"
	AssessmentClassifierUnavailable AssessmentDenial = "classifier_unavailable"
	AssessmentNoClassifierVerdict   AssessmentDenial = "no_classifier_verdict"
)

func RecordAssessmentDenial(ctx context.Context, key, id string, denial AssessmentDenial) error {
	_, err := call(ctx, key, request{Key: key, Operation: "assessment-denied", AssessmentID: id, AssessmentDenial: denial})
	return err
}

func (w *worker) recordAssessmentDenial(ctx context.Context, id string, denial AssessmentDenial) {
	switch denial {
	case AssessmentDenied, AssessmentOversightDenied, AssessmentClassifierUnavailable, AssessmentNoClassifierVerdict:
	default:
		return
	}
	pending := w.hypotheses[id]
	if w.cfg.Agent != "claude" || pending == nil || !w.allowed(ctx, pending.request.Repository) {
		return
	}
	select {
	case <-pending.expired:
		return
	default:
	}
	pending.reply <- HypothesisResponse{RequestID: id, Status: assessmentStatusDenied, denial: denial}
	delete(w.hypotheses, id)
}

var errAgentResponseTimeout = errors.New("agent response timed out after retrieval")
var errAssessmentDenied = errors.New("Claude Code blocked the Quick review assessment submission")

const agentResponseTimeout = 10 * time.Minute

type hypothesisWait struct {
	delivered   chan struct{}
	expired     chan struct{}
	stopOffered bool

	request HypothesisRequest
	reply   chan HypothesisResponse
}

func (p *hypothesisWait) isExpired() bool {
	select {
	case <-p.expired:
		return true
	default:
		return false
	}
}

func SubmitHypotheses(ctx context.Context, key string, response HypothesisResponse) (Response, error) {
	return call(ctx, key, request{Key: key, Operation: "hypotheses", Hypotheses: &response})
}
func (w *worker) recordHypotheses(response HypothesisResponse) error {
	pending := w.hypotheses[response.RequestID]
	if pending == nil {
		return fmt.Errorf("unknown or expired hypothesis request")
	}
	if pending.isExpired() {
		return errAgentResponseTimeout
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
	if response.Hypotheses == nil {
		return fmt.Errorf("hypotheses must be an explicit array")
	}
	if response.Status == "assessed" {
		if len(response.Assessments) != len(pending.request.Areas) {
			return fmt.Errorf("assessment requires %d entries matching the selected areas; received %d", len(pending.request.Areas), len(response.Assessments))
		}
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

// The watcher context owns undelivered requests. Start the assessment budget only
// after retrieval, so ordinary coding work cannot expire an unseen request.
func (p *hypothesisWait) wait(ctx context.Context, timeout time.Duration) (HypothesisResponse, error) {
	var deadline <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	delivered := p.delivered
	for {
		select {
		case <-delivered:
			timer = time.NewTimer(timeout)
			deadline = timer.C
			delivered = nil
		case response := <-p.reply:
			return response, nil
		case <-deadline:
			close(p.expired)
			return HypothesisResponse{}, errAgentResponseTimeout
		case <-ctx.Done():
			return HypothesisResponse{}, ctx.Err()
		}
	}
}
