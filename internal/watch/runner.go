package watch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

var errReviewTerminal = errors.New("review finished without a look closer review")

type Client interface {
	UploadWorkspaceTree(context.Context, string, string, []sourcesnapshot.Entry, map[string][]byte) (string, string, error)
	StartWorkspace(context.Context, string, adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error)
	GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error)
	ReportWorkspaceLead(context.Context, string, string, string, string, string, *bool) error
}
type Runner struct {
	Root         string
	Config       Config
	Token        string
	Client       Client
	Output       io.Writer
	Interval     time.Duration
	Base         string
	Debug        bool
	StartedAt    time.Time
	WatchSession string
	WatchToken   string
}
type revision struct {
	Session    string
	RequestKey string
	ID         string

	Group  string
	Number int
	Digest string
}

func (r Runner) logf(format string, args ...any) {
	if r.Output == nil {
		return
	}
	fmt.Fprintf(r.Output, "[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func (r Runner) debugf(format string, args ...any) {
	if r.Debug {
		fmt.Fprintf(r.Output, "[debug %s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}
}

// A scan is only actionable after an attributable edit in a live agent turn.
// This excludes saved state from before this watcher started and manual edits.
func agentReviewRequested(root, path, fingerprint string, since time.Time) bool {
	var state State
	if Read(StatePath(root, path), &state) != nil || state.Ambiguous || len(state.Active) != 0 || state.Owner.Session == "" || state.Owner.Turn == "" || state.Fingerprint != fingerprint || state.RequestedAt.IsZero() || state.RequestedAt.Before(since) {
		return false
	}
	var turn Turn
	return Read(TurnPath(root, state.Owner.Session), &turn) == nil && turn.Owner == state.Owner
}

// Full snapshots are only useful for a live review request or a completed
// tool whose capture needs recovery. Filesystem noise alone cannot authorize one.
func captureRequested(root, path string, since time.Time, reviewed string) bool {
	var state State
	if Read(StatePath(root, path), &state) != nil || state.Ambiguous {
		return false
	}
	var owner Owner
	if len(state.Active) == 1 {
		for _, op := range state.Active {
			if op.Attributable && !op.CompletedAt.IsZero() && !op.CompletedAt.Before(since) && time.Since(op.Started) <= 30*time.Minute {
				owner = op.Owner
			}
		}
	} else if len(state.Active) == 0 && !state.RequestedAt.IsZero() && !state.RequestedAt.Before(since) && state.Fingerprint != reviewed {
		owner = state.Owner
	}
	var turn Turn
	return owner.Session != "" && owner.Turn != "" && Read(TurnPath(root, owner.Session), &turn) == nil && turn.Owner == owner
}

func (r Runner) Run(ctx context.Context, path string, once bool) error {
	reportingContext, stopReporting := context.WithCancel(ctx)
	if r.Config.ActivationReporting {
		reportingDone := make(chan struct{})
		go func() { defer close(reportingDone); r.runActivationReporting(reportingContext, path) }()
		defer func() { stopReporting(); <-reportingDone }()
	} else {
		defer stopReporting()
	}

	if ctx.Err() != nil || RepositoryRemoved(r.Root, path) {
		return nil
	}
	started := r.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	var last, observed string
	var lastRequest *State
	var nextRetry time.Time
	var retryRequest *State
	var retryCapture sourcesnapshot.Capture
	var retryAttempts int
	var baselineOwner Owner
	var lastHookDiagnostic time.Time
	if r.Interval <= 0 {
		r.Interval = time.Minute
	}
	var events *filesystemEvents
	if !once {
		var eventErr error
		events, eventErr = newFilesystemEvents(ctx, r, path)
		if eventErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			r.logf("%s: filesystem notifications unavailable; checking every %s (%s).", filepath.Base(path), r.Interval, eventErr)
		} else {
			defer events.watcher.Close()
			r.debugf("%s: filesystem notifications enabled; fallback scan every %s", path, r.Interval)
		}
	}
	if r.WatchSession != "" && ctx.Err() == nil {
		_ = SetWatchRuntime(r.Root, path, WatchRuntime{Session: r.WatchSession, Token: r.WatchToken, Status: "Watching"})
	}
	for {
		var nextWake time.Duration
		if ctx.Err() != nil || RepositoryRemoved(r.Root, path) {
			return nil
		}
		if retryRequest != nil && time.Now().After(nextRetry) {
			var current State
			if Read(StatePath(r.Root, path), &current) != nil || !sameReviewRequest(current, *retryRequest) || current.Ambiguous {
				retryRequest = nil
				retryAttempts = 0
			} else if retryReviewRequested(r.Root, path, retryCapture.Digest, *retryRequest) {
				r.logf("%s: retrying the pending review.", filepath.Base(path))
				err := r.reviewForRequest(ctx, path, retryCapture, retryRequest)
				retryAttempts++
				if err == nil || errors.Is(err, errReviewTerminal) || retryAttempts >= 3 {
					last = retryCapture.Digest
					completed := *retryRequest
					lastRequest = &completed
					retryRequest = nil
					if err != nil {
						r.logf("%s: review failed after %d attempts (%s).", filepath.Base(path), retryAttempts, err)
					}
				} else {
					nextRetry = time.Now().Add(15 * time.Second)
					r.markRetrying(path)
				}
				if !events.wait(ctx, r.Interval, 15*time.Second) {
					return nil
				}
				continue
			}
		}
		if !once && retryRequest == nil {
			r.prepareBaseline(ctx, path, started, &baselineOwner)
		}
		if r.Debug && events == nil {
			lastHookDiagnostic = r.logHookDiagnostics(path, lastHookDiagnostic)
		}
		r.flush(ctx)
		r.refreshUnavailableReview(ctx, path)
		_, _, filesystemTask := allChangesTask(r.Root, path)
		if !once && !captureRequested(r.Root, path, started, last) && !(r.Config.AllChanges && filesystemTask) {
			if retryRequest != nil {
				nextWake = time.Until(nextRetry)
			}
			if !events.wait(ctx, r.Interval, nextWake) {
				return nil
			}
			continue
		}
		if events != nil && time.Since(events.refreshedAt) >= r.Interval {
			if err := events.addTree(ctx, path); err != nil {
				r.debugf("%s: refreshing directory watches failed: %s", path, err)
			}
			events.refreshedAt = time.Now()
		}
		r.debugf("%s: scanning working tree", path)
		scanStarted := time.Now()
		captureCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		capture, err := captureForReview(captureCtx, r.Root, path, r.Base)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			r.debugf("%s: scan finished in %s: %s", path, time.Since(scanStarted).Round(time.Millisecond), err)
			// A clean checkout is normal. Other errors are shown once until they change.
			message := err.Error()
			if strings.Contains(message, "worktree changed while capturing") {
				message = "files changed during capture; retrying after they settle"
			}
			if !strings.Contains(message, "snapshot contains no changes") && message != observed {
				r.logf("%s: %s", path, message)
			}
			observed = message
			if strings.Contains(err.Error(), "worktree changed while capturing") {
				nextWake = filesystemDebounce
			}
			if once {
				if strings.Contains(message, "snapshot contains no changes") {
					r.logf("No changes to review.")
					return nil
				}
				return err
			}
		} else if capture.Source.Dirty || once || r.Config.AllChanges {
			var resumed bool
			var recoveryErr error
			if r.Config.AllChanges {
				_, recoveryErr = r.queueAllChanges(ctx, path, capture)
			} else {
				resumed, recoveryErr = resumeCompletedEdit(ctx, r.Root, path, capture, started)
			}
			if errors.Is(recoveryErr, errCaptureEditScope) {
				r.logf("%s: edit capture recovery failed: %s", filepath.Base(path), recoveryErr)
			} else if recoveryErr != nil {
				r.debugf("%s: could not update capture recovery: %s", path, recoveryErr)
			}
			if resumed {
				r.logf("%s: files settled; resuming the agent review.", filepath.Base(path))
			}
			r.debugf("%s: snapshot captured in %s", path, time.Since(scanStarted).Round(time.Millisecond))
			requested := agentReviewRequested(r.Root, path, capture.Digest, started)
			if r.Config.AllChanges {
				requested = agentReviewRequested(r.Root, path, capture.Digest, time.Time{})
			}
			if retryRequest != nil {
				var current State
				if Read(StatePath(r.Root, path), &current) != nil || !sameReviewRequest(current, *retryRequest) {
					retryRequest = nil
					nextRetry = time.Time{}
				} else if retryReviewRequested(r.Root, path, capture.Digest, *retryRequest) {
					requested = true
				}
			}
			if observed != capture.Digest {
				if requested {
					if r.Config.AllChanges {
						r.logf("%s: source change detected.", filepath.Base(path))
					} else {
						r.logf("%s: agent change detected.", filepath.Base(path))
					}
				} else {
					r.debugf("%s: local snapshot recorded; waiting for an agent edit", path)
				}
				observed = capture.Digest
			}
			if !requested {
				r.debugf("%s: no new review request from an agent; no upload", path)
				if once {
					return nil
				}
			} else if !once && awaitingHypotheses(r.Root, path) {
				r.debugf("%s: waiting briefly for agent review hypotheses", path)
				var state State
				if Read(StatePath(r.Root, path), &state) == nil {
					nextWake = time.Until(hypothesisDeadline(state))
				}
				if nextWake <= 0 {
					nextWake = time.Millisecond
				}
			} else if (capture.Digest != last || r.Config.AllChanges && newPromptRequestsReview(r.Root, path, lastRequest)) && time.Now().After(nextRetry) {
				var attempt State
				if e := Read(StatePath(r.Root, path), &attempt); e != nil {
					r.debugf("%s: could not read the pending request: %v", path, e)
				}
				if retryRequest != nil {
					r.logf("%s: retrying the pending review.", filepath.Base(path))
				}
				err = r.reviewForRequest(ctx, path, capture, &attempt)
				if ctx.Err() != nil {
					return nil
				}
				if err == nil {
					last = capture.Digest
					lastRequest = &attempt
					retryRequest = nil
					nextRetry = time.Time{}
				} else if errors.Is(err, errReviewTerminal) {
					// Reposting the same ID returns the same terminal review.
					// A later prompt creates a fresh request, even for this snapshot.
					last = capture.Digest
					lastRequest = &attempt
					retryRequest = nil
					if r.Config.AllChanges {
						r.logf("%s: %s. Waiting for a new prompt or change.", filepath.Base(path), err)
					} else {
						r.logf("%s: %s. Waiting for a new change.", filepath.Base(path), err)
					}
				} else {
					retryRequest = &attempt
					retryCapture = capture
					retryAttempts = 1
					r.logf("%s: review unavailable; retrying in 15 seconds while watching for edits (%s)", path, err)
					nextRetry = time.Now().Add(15 * time.Second)
					nextWake = 15 * time.Second
					r.markRetrying(path)
				}
				if once {
					return err
				}
			} else if capture.Digest == last {
				r.debugf("%s: unchanged since last review", path)
			} else if time.Now().Before(nextRetry) {
				r.debugf("%s: retry in %s", path, time.Until(nextRetry).Round(time.Second))
				nextWake = time.Until(nextRetry)
			} else {
				r.debugf("%s: waiting for files to settle", path)
			}
		} else {
			r.debugf("%s: working tree clean", path)
		}
		if !events.wait(ctx, r.Interval, nextWake) {
			return nil
		}
	}
}
func (r Runner) review(ctx context.Context, path string, capture sourcesnapshot.Capture) error {
	return r.reviewForRequest(ctx, path, capture, nil)
}

func (r Runner) reviewForRequest(ctx context.Context, path string, capture sourcesnapshot.Capture, expected *State) (err error) {
	reviewStarted := time.Now()
	phase := "upload"
	var requestState State
	if Read(StatePath(r.Root, path), &requestState) != nil || requestState.Fingerprint != capture.Digest || requestState.Ambiguous {
		return fmt.Errorf("agent request changed before review")
	}
	if expected != nil && (!sameReviewRequest(requestState, *expected) || hasActiveEdit(requestState)) {
		return fmt.Errorf("agent request changed before review")
	}
	if strings.TrimSpace(requestState.AgentDiff) == "" {
		if writeErr := Write(FeedPath(r.Root, path), Feed{Owner: requestState.Owner, Fingerprint: capture.Digest, Updated: time.Now(), AssessmentStatus: "unavailable", FailureCode: "capture_edit_scope"}); writeErr != nil {
			return writeErr
		}
		return fmt.Errorf("%w: agent edit scope is empty", errReviewTerminal)
	}
	defer func() {
		if err != nil {
			// Release a hook immediately on upload/request failure. The feed
			// belongs only to this edit; a successful retry can replace it.
			code := phase + "_failed"
			if errors.Is(err, adversarylabs.ErrMissingReviewScope) {
				code = "missing_review_scope"
			}
			if errors.Is(err, context.DeadlineExceeded) {
				code = phase + "_timeout"
			}
			_ = Write(FeedPath(r.Root, path), Feed{Owner: requestState.Owner, Fingerprint: capture.Digest, Updated: time.Now(), AssessmentStatus: "unavailable", FailureCode: code})
		}
	}()
	_ = Write(FeedPath(r.Root, path), Feed{Owner: requestState.Owner, Fingerprint: capture.Digest, Updated: time.Now(), AssessmentStatus: "uploading"})
	manifest, err := sourcesnapshot.DecodeWorkspace(capture.Bytes)
	if err != nil {
		return err
	}
	diff, err := Diff(ctx, manifest)
	if err != nil {
		return err
	}
	repo := capture.Source.Repository
	if repo == "" {
		repo = sourcesnapshot.LocalRepositoryIdentifier(path)
	}
	uploadStarted := time.Now()
	uploadCtx, uploadCancel := context.WithTimeout(ctx, workspaceUploadTimeout)
	baseRepo, baseID, headID, err := r.uploadSnapshots(uploadCtx, path, repo, manifest)
	uploadCancel()
	if err != nil {
		return err
	}
	r.logf("%s: upload completed in %.1f seconds.", filepath.Base(path), time.Since(uploadStarted).Seconds())
	phase = "analysis"
	_ = Write(FeedPath(r.Root, path), Feed{Owner: requestState.Owner, Fingerprint: capture.Digest, Updated: time.Now(), AssessmentStatus: "analyzing"})
	// Uploading a new repository must not consume the assessment's budget.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	var rev revision
	revisionIdentity := path + "\x00" + requestState.Owner.Session
	if r.Config.InstallationAttemptID != "" {
		revisionIdentity += "\x00" + r.Config.InstallationAttemptID
	}
	revPath := filepath.Join(r.Root, "revisions", Key(revisionIdentity)+".json")
	if err := Read(revPath, &rev); os.IsNotExist(err) && r.Config.InstallationAttemptID == "" {
		// The old path is reusable only when its request identity proves the
		// same edit, including the owning session. Other legacy groups reset.
		_ = Read(filepath.Join(r.Root, "revisions", Key(path)+".json"), &rev)
	}
	rev = revisionForEdit(rev, capture.Digest, requestState)

	if err = Write(revPath, rev); err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	input := adversarylabs.WorkspaceSubmission{GitIdentity: capture.GitIdentity, Hypotheses: requestState.Hypotheses, AgentDiff: requestState.AgentDiff, RepositoryID: baseRepo, SnapshotID: headID, BaseSnapshotID: baseID, ReviewID: rev.ID, GroupID: rev.Group, Revision: rev.Number, Repository: repo, Branch: capture.Source.Branch, Diff: diff, Continuity: true, Client: map[string]string{"name": "Doomer CLI", "hostname": hostname, "agent": requestState.Agent}}
	if r.Config.AllChanges {
		look, deep := true, false
		input.LookCloser, input.FullReview, input.ReviewScope = &look, &deep, "diff"
	}
	if r.Config.ActivationReporting {
		input.Client["installation_id"] = r.Config.InstallationID
		if r.Config.InstallationAttemptID != "" {
			input.Client["installation_attempt_id"] = r.Config.InstallationAttemptID
		}
	}
	if prompt := strings.TrimSpace(requestState.Prompt); prompt != "" {
		text := []rune(prompt)
		if len(text) > 4000 {
			text = text[:4000]
		}
		input.UserRequest = &adversarylabs.WorkspaceUserRequest{Prompt: string(text), SessionID: requestState.Owner.Session, TurnID: requestState.Owner.Turn}
	}
	r.debugf("%s: starting review revision %d", path, rev.Number)
	submitStarted := time.Now()
	current, err := r.Client.StartWorkspace(ctx, r.Token, input)
	if errors.Is(err, adversarylabs.ErrMissingReviewScope) {
		return fmt.Errorf("%w: %w", errReviewTerminal, err)
	}
	r.debugf("%s: review submission took %s; total preparation %s", path, time.Since(submitStarted).Round(time.Millisecond), time.Since(reviewStarted).Round(time.Millisecond))
	if err != nil {
		return err
	}
	link := adversarylabs.WorkspaceLink(r.Config.API, current)
	r.logf("%s: look closer review requested — %s", filepath.Base(path), link)

	for {
		earlyStatus := "pending"
		if current.Early != nil {
			earlyStatus = current.Early.Status
			if earlyStatus == "running" && current.Early.Stage == "repository_context" {
				_ = Write(FeedPath(r.Root, path), Feed{Owner: requestState.Owner, Fingerprint: capture.Digest, ReviewID: current.ID, URL: link, Updated: time.Now(), AssessmentStatus: "preparing"})
			}
		}
		r.debugf("%s: review status=%s, look closer review=%s", path, current.Status, earlyStatus)
		if current.Early != nil && current.Early.Status == "ready" {
			queueLifecycle(ctx, r.Root, path, requestState.Agent, "first_review", current.ID, r.Config)
		}
		if current.Early != nil && (current.Early.Status == "ready" || current.Early.Status == "unavailable") {
			feed := Feed{BaseRef: manifest.BaseRef, TargetTreeHash: capture.TargetTreeHash, Owner: requestState.Owner, Fingerprint: capture.Digest, ReviewID: current.ID, URL: link, Updated: time.Now(), AssessmentStatus: current.Early.Status, FailureCode: current.Early.FailureCode, Anonymous: current.Anonymous, FullReviewStatus: workspaceFullStatus(current), StatusCheckedAt: time.Now()}
			for _, l := range current.Early.Leads {
				_, idErr := hex.DecodeString(l.ID)
				if len(feed.Leads) >= 10 {
					break
				}
				if idErr != nil || strings.ToLower(l.ID) != l.ID || len(l.Concern) > 4000 || len(l.Check) > 4000 || len(l.ID) != 64 || !sourcesnapshot.ValidPath(l.Path) || l.Line < 1 || strings.TrimSpace(l.Concern) == "" || strings.TrimSpace(l.Check) == "" {
					continue
				}
				feed.Leads = append(feed.Leads, Lead{l.ID, l.Path, l.Line, l.Concern, l.Check})
			}
			if err = Write(FeedPath(r.Root, path), feed); err != nil {
				return err
			}
			if current.Early.Status == "unavailable" {
				r.logf("%s: look closer review unavailable after %.1f seconds — %s", filepath.Base(path), time.Since(submitStarted).Seconds(), link)
			} else {
				totalStarted := reviewStarted
				if !requestState.RequestedAt.IsZero() && requestState.RequestedAt.Before(reviewStarted) {
					totalStarted = requestState.RequestedAt
				}
				areaWord := "areas"
				if len(feed.Leads) == 1 {
					areaWord = "area"
				}
				r.logf("%s: look closer review completed in %.1f seconds (%d %s; %.1f seconds since the agent edit) — %s", filepath.Base(path), time.Since(submitStarted).Seconds(), len(feed.Leads), areaWord, time.Since(totalStarted).Seconds(), link)
			}
			return nil
		}
		if current.Status == "failed" || current.Status == "cancelled" || current.Status == "completed" {
			return fmt.Errorf("%w: full review %s", errReviewTerminal, current.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		current, err = r.Client.GetWorkspace(ctx, r.Token, current.ID)
		if err != nil {
			return err
		}
	}
}
func (r Runner) flush(ctx context.Context) {
	_ = Locked(r.Root, "feedback-upload", func() error {
		entries, _ := os.ReadDir(filepath.Join(r.Root, "feedback"))
		for index, e := range entries {
			if index >= 3 {
				break
			}
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			file := filepath.Join(r.Root, "feedback", e.Name())
			var f Feedback
			if Read(file, &f) != nil {
				continue
			}
			sendCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := r.Client.ReportWorkspaceLead(sendCtx, r.Token, f.ReviewID, f.ID, f.Status, f.Reason, f.Agrees)
			cancel()
			if err == nil {
				r.debugf("agent response uploaded for review %s", f.ReviewID)
				_ = os.Remove(file)
			} else {
				r.debugf("agent response upload failed; kept for retry: %s", err)
			}
		}

		return nil
	})
}

// Content can return to an earlier value after undo/reapply. Request identity
// also includes the owning edit and its context; transport retries keep the key.
func revisionForEdit(rev revision, digest string, state State) revision {
	raw, _ := json.Marshal(struct {
		Digest      string
		Owner       Owner
		RequestedAt time.Time
		AgentDiff   string
		Prompt      string
		Hypotheses  []adversarylabs.WorkspaceHypothesis
	}{digest, state.Owner, state.RequestedAt, state.AgentDiff, state.Prompt, state.Hypotheses})
	key := Key(string(raw))
	if rev.Session == "" && rev.RequestKey == key {
		rev.Session = state.Owner.Session
	}
	if rev.Session != state.Owner.Session {
		rev = revision{Session: state.Owner.Session}
	}
	if rev.Group == "" {
		rev.Group = rand.Text()
	}
	if rev.ID == "" || rev.RequestKey != key {
		rev.Number++
		rev.ID = rand.Text()
		rev.Digest = digest
		rev.RequestKey = key
	}
	return rev
}
