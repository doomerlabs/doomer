package livewatch

import (
	"context"
	"fmt"
	"github.com/doomerlabs/doomer/internal/version"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"time"
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
func (l *reviewLifecycle) event(stage, state string) {
	if l == nil {
		return
	}
	offset := time.Since(l.started).Milliseconds()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = l.client.ReportWorkspaceLifecycle(ctx, l.cfg.Token, map[string]any{"review_id": l.id, "repository": l.repository, "repository_id": l.repoID, "snapshot_id": l.snapshot, "branch": l.branch, "user_request": l.cfg.Request, "client": map[string]string{"name": "Doomer CLI", "agent": l.cfg.Agent, "commit": version.Commit}, "event": map[string]any{"stage": stage, "state": state, "offset_ms": offset}})
}
