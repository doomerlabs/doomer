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
	Root      string
	Config    Config
	Token     string
	Client    Client
	Output    io.Writer
	Interval  time.Duration
	Base      string
	Debug     bool
	StartedAt time.Time
}
type revision struct {
	RequestKey string
	ID         string

	Group  string
	Number int
	Digest string
}

func (r Runner) logf(format string, args ...any) {
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

func (r Runner) Run(ctx context.Context, path string, once bool) error {
	started := r.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	var last, observed string
	var nextRetry time.Time
	var lastHookDiagnostic time.Time
	if r.Interval <= 0 {
		r.Interval = time.Minute
	}
	var events *filesystemEvents
	if !once {
		var eventErr error
		events, eventErr = newFilesystemEvents(ctx, r, path)
		if eventErr != nil {
			r.logf("%s: filesystem notifications unavailable; checking every %s (%s).", filepath.Base(path), r.Interval, eventErr)
		} else {
			defer events.watcher.Close()
			r.debugf("%s: filesystem notifications enabled; fallback scan every %s", path, r.Interval)
		}
	}
	for {
		var nextWake time.Duration
		if ctx.Err() != nil {
			return nil
		}
		if r.Debug && events == nil {
			lastHookDiagnostic = r.logHookDiagnostics(path, lastHookDiagnostic)
		}
		if events != nil && time.Since(events.refreshedAt) >= r.Interval {
			if err := events.addTree(ctx, path); err != nil {
				r.debugf("%s: refreshing directory watches failed: %s", path, err)
			}
			events.refreshedAt = time.Now()
		}
		r.flush(ctx)
		r.debugf("%s: scanning working tree", path)
		scanStarted := time.Now()
		captureCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		capture, err := sourcesnapshot.CaptureWorktree(captureCtx, sourcesnapshot.CaptureOptions{Workspace: true, Path: path, Base: r.Base})
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
		} else if capture.Source.Dirty || once {
			if resumeCompletedEdit(ctx, r.Root, path, capture, started) {
				r.logf("%s: files settled; resuming the agent review.", filepath.Base(path))
			}
			r.debugf("%s: snapshot captured in %s", path, time.Since(scanStarted).Round(time.Millisecond))
			requested := agentReviewRequested(r.Root, path, capture.Digest, started)
			if observed != capture.Digest {
				if requested {
					r.logf("%s: agent change detected.", filepath.Base(path))
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
					nextWake = time.Until(state.RequestedAt.Add(hypothesisGrace))
				}
				if nextWake <= 0 {
					nextWake = time.Millisecond
				}
			} else if capture.Digest != last && time.Now().After(nextRetry) {
				reviewCtx, stop := context.WithTimeout(ctx, 2*time.Minute)
				err = r.review(reviewCtx, path, capture)
				stop()
				if ctx.Err() != nil {
					return nil
				}
				if err == nil {
					last = capture.Digest
				} else if errors.Is(err, errReviewTerminal) {
					// Reposting the same ID returns the same terminal review.
					// Wait for a changed snapshot instead of retrying forever.
					last = capture.Digest
					r.logf("%s: %s. Waiting for a new change.", filepath.Base(path), err)
				} else {
					r.logf("%s: review unavailable; retrying later (%s)", path, err)
					nextRetry = time.Now().Add(15 * time.Second)
					nextWake = 15 * time.Second
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
func (r Runner) review(ctx context.Context, path string, capture sourcesnapshot.Capture) (err error) {
	reviewStarted := time.Now()
	var requestState State
	if Read(StatePath(r.Root, path), &requestState) != nil || requestState.Fingerprint != capture.Digest || requestState.Ambiguous {
		return fmt.Errorf("agent request changed before review")
	}
	defer func() {
		if err != nil {
			// Release a hook immediately on upload/request failure. The feed
			// belongs only to this edit; a successful retry can replace it.
			_ = Write(FeedPath(r.Root, path), Feed{Owner: requestState.Owner, Fingerprint: capture.Digest, Updated: time.Now(), AssessmentStatus: "unavailable"})
		}
	}()
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
		repo = "local/" + filepath.Base(path)
	}
	sourceBytes := 0
	for _, data := range manifest.Blobs {
		sourceBytes += len(data)
	}
	r.logf("%s: uploading snapshots for review (%s source; only missing content is sent).", filepath.Base(path), formatUploadBytes(sourceBytes))
	uploadStarted := time.Now()
	baseRepo, baseID, headID, err := r.uploadSnapshots(ctx, path, repo, manifest)
	if err != nil {
		return err
	}
	r.logf("%s: upload completed in %.1f seconds.", filepath.Base(path), time.Since(uploadStarted).Seconds())

	var rev revision
	revPath := filepath.Join(r.Root, "revisions", Key(path)+".json")
	_ = Read(revPath, &rev)
	rev = revisionForEdit(rev, capture.Digest, requestState)

	if err = Write(revPath, rev); err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	input := adversarylabs.WorkspaceSubmission{Hypotheses: requestState.Hypotheses, AgentDiff: requestState.AgentDiff, RepositoryID: baseRepo, SnapshotID: headID, BaseSnapshotID: baseID, ReviewID: rev.ID, GroupID: rev.Group, Revision: rev.Number, Repository: repo, Branch: capture.Source.Branch, Diff: diff, Continuity: true, Client: map[string]string{"name": "Doomer CLI", "hostname": hostname, "agent": requestState.Agent}}
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
	r.debugf("%s: review submission took %s; total preparation %s", path, time.Since(submitStarted).Round(time.Millisecond), time.Since(reviewStarted).Round(time.Millisecond))
	if err != nil {
		return err
	}
	link := strings.TrimSuffix(r.Config.API, "/api") + "/worktree/reviews/" + current.ID
	r.logf("%s: look closer review requested — %s", filepath.Base(path), link)

	for {
		earlyStatus := "pending"
		if current.Early != nil {
			earlyStatus = current.Early.Status
		}
		r.debugf("%s: review status=%s, look closer review=%s", path, current.Status, earlyStatus)
		if current.Early != nil && (current.Early.Status == "ready" || current.Early.Status == "unavailable") {
			feed := Feed{Owner: requestState.Owner, Fingerprint: capture.Digest, ReviewID: current.ID, URL: link, Updated: time.Now(), AssessmentStatus: current.Early.Status}
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
