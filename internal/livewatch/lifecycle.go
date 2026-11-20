package livewatch

import (
	"context"
	"errors"
	"fmt"
	"github.com/doomerlabs/doomer/internal/version"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"strings"
	"time"
	"unicode/utf8"
)

type lifecycleClient interface {
	ReportWorkspaceLifecycle(context.Context, string, any) error
}
type reviewLifecycle struct {
	client                                   lifecycleClient
	cfg                                      Config
	id, repository, repoID, snapshot, branch string
	started                                  time.Time
}

func newReviewLifecycle(client Client, cfg Config, repo string, capture sourcesnapshot.Capture, revision int, started time.Time) *reviewLifecycle {
	c, ok := client.(lifecycleClient)
	if !ok || !cfg.RequireHypotheses {
		return nil
	}
	name := capture.Source.Repository
	if name == "" {
		name = sourcesnapshot.LocalRepositoryIdentifier(repo)
	}
	return &reviewLifecycle{c, cfg, reviewIdentity(cfg, repo, revision), name, sourcesnapshot.Digest([]byte(name)), capture.TargetTreeHash, capture.Source.Branch, started}
}
func reviewIdentity(cfg Config, repo string, revision int) string {
	return Key(Key(cfg.ID+"\x00"+repo) + "\x00" + fmt.Sprint(revision))
}
func (l *reviewLifecycle) failure(stage string, err error, assessment *HypothesisResponse) {
	if l == nil {
		return
	}
	code := "review_error"
	if stage == "agent_assessment" {
		code = "handoff_error"
	}
	var response *adversarylabs.ResponseError
	if errors.Is(err, errAgentResponseTimeout) || errors.Is(err, adversarylabs.ErrAgentAssessmentExpired) {
		code = "agent_response_timeout"
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "deadline_exceeded"
	} else if errors.Is(err, context.Canceled) {
		code = "cancelled"
	} else if errors.As(err, &response) && (strings.HasPrefix(response.Code, "triage_") || strings.HasPrefix(response.Code, "invalid_triage_")) {
		code = response.Code
	}
	details := map[string]any{"error_code": code, "reason": boundedFailureReason(err.Error())}
	if assessment != nil {
		if assessment.Status == "superseded" || assessment.Status == "incomplete" {
			details["error_code"] = assessment.Status
		}
		if assessment.RequestID != "" {
			details["request_id"] = assessment.RequestID
		}
		if assessment.denial != "" {
			details["error_code"] = "agent_permission_denied"
			details["reason"] = string(assessment.denial)
		}
	}
	l.event(stage, "failed", details)
}

func boundedFailureReason(reason string) string {
	if len(reason) > 1024 {
		reason = reason[:1024]
	}
	for !utf8.ValidString(reason) {
		reason = reason[:len(reason)-1]
	}
	return reason
}

func (l *reviewLifecycle) event(stage, state string, details ...map[string]any) {
	if l == nil {
		return
	}
	offset := time.Since(l.started).Milliseconds()
	event := map[string]any{"stage": stage, "state": state, "offset_ms": offset}
	if len(details) > 0 {
		for key, value := range details[0] {
			event[key] = value
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	payload := map[string]any{"review_id": l.id, "event": event}
	if event["error_code"] != "agent_permission_denied" {
		payload["repository"], payload["repository_id"], payload["snapshot_id"], payload["branch"] = l.repository, l.repoID, l.snapshot, l.branch
		payload["user_request"] = l.cfg.Request
		payload["client"] = map[string]string{"name": "Doomer CLI", "agent": l.cfg.Agent, "commit": version.Commit}
	}
	_ = l.client.ReportWorkspaceLifecycle(ctx, l.cfg.Token, payload)
}
