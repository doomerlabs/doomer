package livewatch

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"github.com/fsnotify/fsnotify"
)

type repository struct {
	baseline map[string]initialFile

	observed          string
	pending, running  bool
	version           int
	retriedAssessment bool
	frozen            *sourcesnapshot.Capture
	result            *Result
	cancel            context.CancelFunc
}
type completion struct {
	path    string
	version int
	result  Result
	capture sourcesnapshot.Capture
}
type control struct {
	assessmentID     string
	assessmentDenial AssessmentDenial
	hypotheses       *HypothesisResponse

	addDirectory string

	cancel           bool
	cancelAssessment bool
	checkpoint       bool
	finish           bool
	results          bool
	listFindings     bool
	findingDenied    bool
	noticeID         string
	noticeRevision   int
	noticeFromReply  bool
	feedback         []Feedback
	reply            chan Response
	sent             chan struct{}
}
type worker struct {
	background   sync.WaitGroup
	prepareSlots chan struct{}

	hypotheses      map[string]*hypothesisWait
	hypothesisReady chan *hypothesisWait

	cfg                Config
	client             Client
	allowed            func(context.Context, string) bool
	directories        []string
	repos              map[string]*repository
	sourceChanged      bool
	noticeAcks         map[string]bool
	findings           map[string]Finding
	findingToolDenied  bool
	findingStopOffered bool
	watcher            *fsnotify.Watcher
	controls           chan control
	completed          chan completion
	slots              chan struct{}
	jobs               sync.WaitGroup
	closeListener      func() error
}

// Serve runs until its caller stops it and closes its optional control listener.
// The only persistent data it reads is the caller's saved authorization.
func Serve(ctx context.Context, cfg Config, client Client, allowed func(context.Context, string) bool) error {
	var listener net.Listener
	if cfg.ControlKey != "" {
		var err error
		listener, err = listen(cfg.ControlKey)
		if err != nil {
			return err
		}
		defer listener.Close()
	}
	return serve(ctx, listener, cfg, client, allowed)
}

// CheckStartup opens the same local control socket and initializes the same
// filesystem watcher as Serve. It closes both without handling review work.
func CheckStartup(ctx context.Context, cfg Config, checkout string, allowed func(context.Context, string) bool) error {
	listener, err := listen(Key(rand.Text()))
	if err != nil {
		return err
	}
	defer listener.Close()
	w := &worker{directories: []string{cfg.Directory}, cfg: cfg, allowed: allowed, repos: map[string]*repository{}}
	if err := w.initialize(ctx); err != nil {
		return err
	}
	defer w.watcher.Close()
	if w.repos[checkout] == nil {
		return fmt.Errorf("watcher did not establish a baseline for this checkout")
	}
	return nil
}

func (w *worker) initialize(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.watcher = watcher
	startup, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	if err := w.scan(startup, true, false); err != nil {
		watcher.Close()
		return err
	}
	return nil
}

func serve(ctx context.Context, listener net.Listener, cfg Config, client Client, allowed func(context.Context, string) bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &worker{directories: []string{cfg.Directory}, cfg: cfg, client: client, allowed: allowed, repos: map[string]*repository{}, findings: map[string]Finding{}, noticeAcks: map[string]bool{}, controls: make(chan control), completed: make(chan completion, 64), slots: make(chan struct{}, 4)}
	w.prepareSlots = make(chan struct{}, 1)
	w.hypotheses = map[string]*hypothesisWait{}
	w.hypothesisReady = make(chan *hypothesisWait)
	w.closeListener = func() error { return nil }
	if listener != nil {
		w.closeListener = listener.Close
	}
	defer func() { cancel(); w.jobs.Wait(); w.background.Wait() }()
	if err := w.initialize(ctx); err != nil {
		return err
	}
	defer w.watcher.Close()
	if listener != nil {
		go func() { <-ctx.Done(); listener.Close() }()
		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				go func(conn net.Conn) {
					defer conn.Close()
					if !ownPeer(conn) {
						return
					}
					_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
					var req request
					if json.NewDecoder(io.LimitReader(conn, MaxRequestBytes)).Decode(&req) != nil || req.Key != cfg.ControlKey {
						return
					}
					if (req.Operation == "notice-ack" || req.Operation == "notice-reply") && (req.NoticeID == "" || req.NoticeRevision < 1) {
						_ = json.NewEncoder(conn).Encode(Response{Error: "advisory ID and revision are required"})
						return
					}
					if req.Operation == "feedback" && len(req.Feedback) == 0 {
						_ = json.NewEncoder(conn).Encode(Response{Error: "at least one finding response is required"})
						return
					}
					if req.Operation == "add" && req.Directory == "" {
						_ = json.NewEncoder(conn).Encode(Response{Error: "directory is required"})
						return
					}
					if req.Operation == "status" {
						_ = json.NewEncoder(conn).Encode(Response{Ready: true, API: cfg.API, Directory: cfg.Directory})
						return
					}
					if req.Operation == "assessment-denied" && req.AssessmentID == "" {
						return
					}
					switch req.Operation {
					case "assessment-denied", "hypotheses", "add", "stop", "cancel", "cancel-assessment", "checkpoint", "feedback", "finish", "results", "findings", "finding-denied", "notice-ack", "notice-reply":
					default:
						return
					}
					command := control{hypotheses: req.Hypotheses, addDirectory: req.Directory, results: req.Operation == "results", cancel: req.Operation == "cancel", checkpoint: req.Operation == "checkpoint" || req.Operation == "finish", finish: req.Operation == "finish", feedback: req.Feedback, noticeID: req.NoticeID, noticeRevision: req.NoticeRevision, noticeFromReply: req.Operation == "notice-reply", reply: make(chan Response, 1), sent: make(chan struct{})}
					command.cancelAssessment = req.Operation == "cancel-assessment"
					command.listFindings = req.Operation == "findings"
					command.findingDenied = req.Operation == "finding-denied"
					if req.Operation == "assessment-denied" {
						command.assessmentID, command.assessmentDenial = req.AssessmentID, req.AssessmentDenial
					}
					select {
					case w.controls <- command:
					case <-ctx.Done():
						return
					}
					select {
					case result := <-command.reply:
						_ = json.NewEncoder(conn).Encode(result)
					case <-ctx.Done():
					}
					close(command.sent)
				}(conn)
			}
		}()
	}
	if cfg.Stop != nil {
		go func() {
			select {
			case <-cfg.Stop:
			case <-ctx.Done():
				return
			}
			sent := make(chan struct{})
			close(sent)
			select {
			case w.controls <- control{reply: make(chan Response, 1), sent: sent}:
			case <-ctx.Done():
			}
		}()
	}
	return w.run(ctx)
}

func (w *worker) watchDirectories(ctx context.Context, directory string) error {
	return filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if path != directory {
			switch entry.Name() {
			case ".git", "node_modules", ".venv", "__pycache__", ".cache":
				return filepath.SkipDir
			}
		}
		return w.watcher.Add(path)
	})
}

func (w *worker) scan(ctx context.Context, initial, freeze bool) error {
	if !w.allowed(ctx, "") {
		return fmt.Errorf("upload approval was revoked or the installation was removed")
	}
	for _, directory := range w.directories {
		if err := w.scanDirectory(ctx, directory, initial, freeze); err != nil {
			return err
		}
	}
	return nil
}

func (w *worker) scanDirectory(ctx context.Context, directory string, initial, freeze bool) error {
	if !freeze {
		if err := w.watchDirectories(ctx, directory); err != nil {
			return err
		}
	}
	paths, err := DiscoverRepositories(ctx, directory)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if !w.allowed(ctx, path) {
			if old := w.repos[path]; old != nil && old.cancel != nil {
				old.cancel()
			}
			delete(w.repos, path)
			continue
		}
		fingerprint, err := workspaceFingerprint(ctx, path)
		if err != nil {
			continue
		}
		r := w.repos[path]
		if r == nil {
			// Existing changes establish the baseline; only a later change may submit.
			baseline, e := initialEditFiles(ctx, path)
			if e != nil {
				return e
			}
			r = &repository{baseline: baseline, observed: fingerprint, pending: !initial}
			w.repos[path] = r
			w.sourceChanged = w.sourceChanged || !initial
		} else if fingerprint != r.observed {
			r.observed = fingerprint
			r.pending = true
			w.sourceChanged = true
		}
		if freeze && r.pending && !w.cfg.RequireHypotheses {
			capture, err := capture(ctx, path, r.baseline)
			if err != nil {
				r.pending = false
				if !errors.Is(err, sourcesnapshot.ErrNoChanges) {
					r.result = &Result{Repository: path, Status: "unavailable", Error: err.Error()}
				}
			} else {
				r.frozen = &capture
			}
		}
	}
	return nil
}

func capture(ctx context.Context, path string, baseline ...map[string]initialFile) (sourcesnapshot.Capture, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	opts := sourcesnapshot.CaptureOptions{Workspace: true, Path: path}
	if len(baseline) > 0 {
		initial := sourcesnapshot.Manifest{Target: []sourcesnapshot.Entry{}, Blobs: map[string][]byte{}}
		opts.InitialExcluded = map[string]string{}
		for path, f := range baseline[0] {
			if f.excluded != "" {
				opts.InitialExcluded[path] = f.excluded
				continue
			}
			if f.deleted {
				continue
			}
			hash := sourcesnapshot.Digest(f.body)
			initial.Target = append(initial.Target, sourcesnapshot.Entry{Path: path, Hash: hash, Mode: f.mode})
			initial.Blobs[hash] = f.body
		}
		sort.Slice(initial.Target, func(i, j int) bool { return initial.Target[i].Path < initial.Target[j].Path })
		opts.Initial = &initial
	}
	return sourcesnapshot.CaptureWorktree(ctx, opts)
}

func (w *worker) schedule(ctx context.Context) {
	for path, r := range w.repos {
		if !r.pending || r.running {
			continue
		}
		r.pending = false
		if !w.allowed(ctx, path) {
			r.result = &Result{Repository: path, Status: "unavailable", Error: "upload approval was revoked"}
			continue
		}
		var snapshot sourcesnapshot.Capture
		if r.frozen != nil {
			snapshot = *r.frozen
			r.frozen = nil
		} else if !w.cfg.RequireHypotheses {
			var err error
			snapshot, err = capture(ctx, path)
			if err != nil {
				if !errors.Is(err, sourcesnapshot.ErrNoChanges) {
					r.result = &Result{Repository: path, Status: "unavailable", Error: err.Error()}
				}
				continue
			}
		}
		r.version++
		r.running = true
		jobCtx, cancel := context.WithCancel(ctx)
		if !w.cfg.RequireHypotheses {
			cancel()
			jobCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		}
		r.cancel = cancel
		version := r.version
		w.jobs.Add(1)
		baseline := r.baseline
		go func(path string, version int, snapshot sourcesnapshot.Capture) {
			defer w.jobs.Done()
			defer cancel()
			select {
			case w.slots <- struct{}{}:
			case <-jobCtx.Done():
				return
			}
			defer func() { <-w.slots }()
			captureStarted := time.Now()
			if w.cfg.RequireHypotheses && snapshot.Bytes == nil {
				captured, err := capture(jobCtx, path, baseline)
				if err != nil {
					result := Result{Repository: path, Status: "unavailable", Error: err.Error()}
					if errors.Is(err, sourcesnapshot.ErrNoChanges) {
						result.Status, result.Error = "unchanged", ""
					}
					select {
					case w.completed <- completion{path: path, version: version, result: result}:
					case <-ctx.Done():
					}
					return
				}
				snapshot = captured
			}
			cfg := w.cfg
			cfg.editBaseline = baseline
			if cfg.RequireHypotheses {
				manifest, err := sourcesnapshot.DecodeWorkspace(snapshot.Bytes)
				var diff string
				if err == nil {
					diff, err = editScope(jobCtx, manifest, baseline, snapshot.Skipped)
				}
				if err != nil || strings.TrimSpace(diff) == "" {
					result := Result{Repository: path, Status: "unchanged"}
					if err != nil {
						result.Status, result.Error = "unavailable", err.Error()
					}
					select {
					case w.completed <- completion{path: path, version: version, result: result}:
					case <-ctx.Done():
					}
					return
				}
			}
			cfg.lifecycle = newReviewLifecycle(w.client, cfg, path, snapshot, version, captureStarted)
			cfg.lifecycle.event("capture", "completed")
			if cfg.RequireHypotheses {
				w.prepareInBackground(path, snapshot, cfg.lifecycle)
			}
			assessment := &HypothesisResponse{}
			cfg.assessment = assessment
			cfg.hypotheses = func(ctx context.Context, repo, diff string, revision int) ([]adversarylabs.WorkspaceHypothesis, error) {
				var triageResult adversarylabs.WorkspaceTriage
				if triage, ok := w.client.(interface {
					PrepareWorkspaceTriage(context.Context, string, string, string) (adversarylabs.WorkspaceTriage, error)
				}); ok {
					cfg.lifecycle.event("risk_selection", "running")
					var err error
					triageResult, err = triage.PrepareWorkspaceTriage(ctx, cfg.Token, reviewIdentity(cfg, repo, revision), diff)
					if err != nil {
						cfg.lifecycle.failure("risk_selection", err, nil)
						return nil, err
					}
					cfg.lifecycle.event("risk_selection", "completed")
				}
				id := Key(cfg.ID + "\x00" + repo + "\x00" + fmt.Sprint(revision) + "\x00" + diff)
				assessment.RequestID = id
				task := &hypothesisWait{request: HypothesisRequest{ID: id, ReviewID: reviewIdentity(cfg, repo, revision), Revision: revision, Snapshot: snapshot.TargetTreeHash, DiffHash: Key(diff), Repository: repo, Diff: diff, Areas: triageResult.Areas}, reply: make(chan HypothesisResponse, 1), delivered: make(chan struct{}), expired: make(chan struct{})}
				if len(task.request.Diff) > 1<<20 {
					task.request.Diff = task.request.Diff[:1<<20]
					task.request.Partial = true
				}
				select {
				case w.hypothesisReady <- task:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				result, err := task.wait(ctx, agentResponseTimeout)
				if err != nil {
					return nil, err
				}
				result.triageReference = triageResult.DiffReference
				*assessment = result
				if result.Status == assessmentStatusDenied {
					return nil, errAssessmentDenied
				}
				if result.Status != "assessed" {
					return nil, fmt.Errorf("agent assessment %s: %s", result.Status, result.Reason)
				}
				return result.Hypotheses, nil
			}
			result := review(jobCtx, w.client, cfg, path, snapshot, version)
			done := completion{path: path, version: version, result: result}
			if result.assessmentExpired {
				done.capture = snapshot
			}
			select {
			case w.completed <- done:
			case <-ctx.Done():
			}
		}(path, version, snapshot)
	}
}

func (w *worker) response(timeout bool) Response {
	response := Response{Ready: true, API: w.cfg.API, Directory: w.cfg.Directory, FindingToolDenied: w.findingToolDenied}
	for _, pending := range w.hypotheses {
		if !pending.isExpired() {
			response.HypothesisRequests = append(response.HypothesisRequests, pending.request)
		}
	}
	response.OtherReviewPending = w.otherReviewPending(response.HypothesisRequests)
	response.ReviewPending = response.OtherReviewPending || len(response.HypothesisRequests) > 0
	sort.Slice(response.HypothesisRequests, func(i, j int) bool { return response.HypothesisRequests[i].ID < response.HypothesisRequests[j].ID })
	if w.cfg.Consent != nil {
		response.Consent = w.cfg.Consent()
	}
	if w.cfg.Notices != nil {
		for _, notice := range w.cfg.Notices() {
			if w.noticeAcks[fmt.Sprintf("%s:%d", notice.ID, notice.Revision)] {
				notice.NotifyUser = false
			}
			response.Notices = append(response.Notices, notice)
		}
	}

	for path, r := range w.repos {
		if timeout && (r.running || r.pending) {
			response.Results = append(response.Results, Result{Repository: path, Status: "unavailable", Error: "review did not finish before watch stopped"})
		} else if r.result != nil {
			response.Results = append(response.Results, *r.result)
		}
	}
	sort.Slice(response.Results, func(i, j int) bool { return response.Results[i].Repository < response.Results[j].Repository })
	for _, finding := range w.findings {
		response.Findings = append(response.Findings, finding)
	}
	sort.Slice(response.Findings, func(i, j int) bool {
		a, b := response.Findings[i], response.Findings[j]
		return a.ReviewID+a.Lead.ID < b.ReviewID+b.Lead.ID
	})
	return response
}

func (w *worker) otherReviewPending(requests []HypothesisRequest) bool {
	waiting := make(map[string]int, len(requests))
	for _, request := range requests {
		waiting[request.Repository] = request.Revision
	}
	for path, repo := range w.repos {
		revision, hasRequest := waiting[path]
		if repo.pending || (repo.running && (!hasRequest || revision != repo.version)) {
			return true
		}
	}
	return false
}

func (w *worker) collectFindings() {
	for _, repo := range w.repos {
		if repo.result != nil {
			for _, lead := range repo.result.Leads {
				key := repo.result.ID + "/" + lead.ID
				if _, seen := w.findings[key]; !seen {
					w.findings[key] = Finding{Repository: repo.result.Repository, ReviewID: repo.result.ID, URL: repo.result.URL, Lead: lead}
				}
			}
		}
	}
}

func hasPendingFeedback(response Response) bool {
	return len(response.HypothesisRequests) > 0 || hasPendingUserFeedback(response)
}

func hasPendingUserFeedback(response Response) bool {
	for _, notice := range response.Notices {
		if notice.NotifyUser {
			return true
		}
	}

	for _, finding := range response.Findings {
		if finding.Disposition == nil {
			return true
		}
	}
	return false
}

func (w *worker) recordFeedback(ctx context.Context, entries []Feedback) error {
	if len(entries) > 64 {
		return fmt.Errorf("at most 64 finding responses per call")
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		key := entry.ReviewID + "/" + entry.LeadID
		if _, known := w.findings[key]; !known || seen[key] {
			return fmt.Errorf("unknown or duplicate finding response")
		}
		seen[key] = true
		if strings.TrimSpace(entry.Reason) == "" || len(entry.Reason) > 2000 {
			return fmt.Errorf("each finding needs a reason of 1–2000 bytes")
		}
		switch entry.Status {
		case "fixed":
			if entry.Agrees == nil || !*entry.Agrees {
				return fmt.Errorf("fixed requires agrees=true")
			}
		case "dismissed":
			if entry.Agrees == nil || *entry.Agrees {
				return fmt.Errorf("dismissed requires agrees=false")
			}
		case "deferred":
		default:
			return fmt.Errorf("status must be fixed, dismissed, or deferred")
		}
	}
	for _, entry := range entries {
		key := entry.ReviewID + "/" + entry.LeadID
		finding := w.findings[key]
		if !w.allowed(ctx, finding.Repository) {
			return fmt.Errorf("upload approval was revoked")
		}
		if err := w.client.ReportWorkspaceLead(ctx, w.cfg.Token, entry.ReviewID, entry.LeadID, entry.Status, entry.Reason, entry.Agrees); err != nil {
			return fmt.Errorf("record finding response: %w", err)
		}
		entry.Reason = strings.TrimSpace(entry.Reason)
		finding.Disposition = &entry
		w.findings[key] = finding
	}
	return nil
}

func (w *worker) run(ctx context.Context) error {
	var idleTimer *time.Timer
	var idleDeadline <-chan time.Time
	var controlDeadline time.Time
	if w.cfg.IdleTimeout > 0 {
		idleTimer = time.NewTimer(min(w.cfg.IdleTimeout, w.cfg.ControlTimeout))
		defer idleTimer.Stop()
		idleDeadline = idleTimer.C
		controlDeadline = time.Now().Add(w.cfg.ControlTimeout)
	}
	// The Codex watcher sees writes from every process in the workspace.
	// Wait for the turn to claim review work via its pre-answer checkpoint.
	reviewStarted := w.cfg.Agent != "codex" || !w.cfg.RequireHypotheses
	scan := func() error {
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		w.sourceChanged = false
		if err := w.scan(bounded, false, false); err != nil {
			return err
		}
		if idleTimer != nil && w.sourceChanged {
			// Another turn may be editing this workspace. Only the owner can
			// renew the control deadline.
			idleTimer.Reset(min(w.cfg.IdleTimeout, time.Until(controlDeadline)))
		}
		if reviewStarted {
			w.schedule(ctx)
		}
		return nil
	}
	scanInterval := time.Second
	if w.cfg.IdleTimeout > 0 {
		// Filesystem events and checkpoints still trigger scans. Poll less
		// often for changes the backend couldn't report.
		scanInterval = 30 * time.Second
	}
	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()
	var debounce *time.Timer
	var settled <-chan time.Time
	var deadline <-chan time.Time
	var drain *time.Timer
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
		if drain != nil {
			drain.Stop()
		}
	}()
	var closing []control
	// Once Stop is received, only explicit checkpoints may capture review fixes.
	// A completed/timed-out turn must never observe edits from a later prompt.
	turnEnded := false
	var terminalTimer *time.Timer
	var terminalDeadline <-chan time.Time
	defer func() {
		if terminalTimer != nil {
			terminalTimer.Stop()
		}
	}()
	finish := func(command control, response Response) {
		if command.finish && len(response.HypothesisRequests) > 0 {
			response.RepeatedStopHandoff = w.repeatStopHandoff()
		}
		if w.cfg.Agent == "claude" {
			w.prepareClaudeFindingHandoff(command, &response)
		}
		command.reply <- response
		select {
		case <-command.sent:
		case <-time.After(time.Second):
		}
	}
	for {
		if turnEnded && len(closing) == 0 {
			w.collectFindings()
			busy := false
			for _, r := range w.repos {
				busy = busy || r.running || r.pending
			}
			if busy || hasPendingFeedback(w.response(false)) {
				if terminalTimer != nil {
					terminalTimer.Stop()
					terminalTimer = nil
					terminalDeadline = nil
				}
			} else if terminalTimer == nil {
				// Briefly retain the report so the agent can read the terminal result.
				terminalTimer = time.NewTimer(15 * time.Second)
				terminalDeadline = terminalTimer.C
			}
		}
		if len(closing) > 0 {
			busy := false
			for _, r := range w.repos {
				busy = busy || r.running || r.pending
			}
			if !busy && w.cfg.NoticesReady != nil {
				select {
				case <-w.cfg.NoticesReady:
				default:
					busy = true
				}
			}
			w.collectFindings()
			response := w.response(false)
			if !busy || len(response.HypothesisRequests) > 0 {
				stop := false
				for _, command := range closing {
					stop = stop || !command.checkpoint || (command.finish && !hasPendingFeedback(response))
				}
				if stop {
					_ = w.closeListener()
				}
				for _, command := range closing {
					if command.checkpoint && !command.finish {
						w.markHypothesesDelivered()
					}
					finish(command, response)
				}
				if stop {
					return nil
				}
				closing = nil
				if drain != nil {
					drain.Stop()
				}
				deadline = nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idleDeadline:
			return nil
		case <-terminalDeadline:
			_ = w.closeListener()
			return nil
		case task := <-w.hypothesisReady:
			w.hypotheses[task.request.ID] = task
		case command := <-w.controls:
			// Status probes bypass this loop and do not extend either deadline.
			if idleTimer != nil {
				controlDeadline = time.Now().Add(w.cfg.ControlTimeout)
				idleTimer.Reset(min(w.cfg.IdleTimeout, w.cfg.ControlTimeout))
			}
			if command.findingDenied {
				w.findingToolDenied = true
				finish(command, Response{})
				continue
			}
			if command.assessmentID != "" {
				w.recordAssessmentDenial(ctx, command.assessmentID, command.assessmentDenial)
				finish(command, Response{})
				continue
			}
			if command.finish {
				turnEnded = true
			}
			if command.checkpoint && !turnEnded {
				reviewStarted = true
			}
			if command.hypotheses != nil {
				err := w.recordHypotheses(*command.hypotheses)
				response := w.response(false)
				if err != nil {
					response.Error = err.Error()
				}
				finish(command, response)
				continue
			}

			if command.addDirectory != "" {
				response := w.response(false)
				if len(closing) != 0 {
					response.Error = "watcher is finishing; cannot add a directory"
				} else {
					bounded, cancel := context.WithTimeout(ctx, 20*time.Second)
					err := w.addDirectory(bounded, command.addDirectory)
					cancel()
					if err != nil {
						response.Error = err.Error()
					}
				}
				finish(command, response)
				continue
			}
			if command.noticeID != "" || command.noticeRevision != 0 {
				response := w.response(false)
				known, blocking := false, false
				for _, n := range response.Notices {
					if n.ID == command.noticeID && n.Revision == command.noticeRevision {
						known, blocking = true, n.Blocking
						break
					}
				}
				if !known || w.cfg.AcknowledgeNotice == nil {
					response.Error = "unknown advisory revision"
				} else if w.cfg.Agent != "" && !blocking && !command.noticeFromReply {
					response.Error = "Optional notice is still pending. Read the full watch results and append its display_markdown to your final reply. The Stop hook records delivery."
				} else if err := w.cfg.AcknowledgeNotice(command.noticeID, command.noticeRevision); err != nil {
					response.Error = err.Error()
				} else {
					w.noticeAcks[fmt.Sprintf("%s:%d", command.noticeID, command.noticeRevision)] = true
					response = w.response(false)
				}
				finish(command, response)
				continue
			}
			if command.results || command.listFindings {
				finish(command, w.response(false))
				continue
			}
			if len(command.feedback) > 0 {
				bounded, stop := context.WithTimeout(ctx, 20*time.Second)
				err := w.recordFeedback(bounded, command.feedback)
				stop()
				response := w.response(false)
				if err != nil {
					response.Error = err.Error()
				}
				finish(command, response)
				continue
			}
			if command.cancelAssessment {
				// Recheck in the worker before canceling. Another review can change
				// state after the Stop response is sent to the hook.
				w.collectFindings()
				response := w.response(false)
				if w.cfg.Agent != "codex" || !turnEnded || response.OtherReviewPending || hasPendingUserFeedback(response) || len(response.HypothesisRequests) == 0 || !w.assessmentsOfferedAtStop() {
					finish(command, response)
					continue
				}
				command.cancel = true
			}
			if command.cancel {
				_ = w.closeListener()
				finish(command, w.response(true))
				return nil
			}
			if len(closing) == 0 {
				timeout := w.cfg.DrainTimeout
				if timeout <= 0 {
					timeout = 35 * time.Second
				}
				drain = time.NewTimer(timeout)
				deadline = drain.C
				if reviewStarted {
					finalCtx, stop := context.WithTimeout(ctx, timeout)
					err := w.scan(finalCtx, false, true)
					stop()
					if err != nil {
						finish(command, Response{Error: fmt.Sprintf("capture final changes: %v", err)})
						return err
					}
				} else {
					for _, repo := range w.repos {
						repo.pending = false
					}
				}
			}
			closing = append(closing, command)
			w.schedule(ctx)
		case <-deadline:
			w.collectFindings()
			response := w.response(true)
			stop := false
			for _, command := range closing {
				stop = stop || !command.checkpoint || (command.finish && !hasPendingFeedback(response))
			}
			if stop {
				_ = w.closeListener()
			}
			for _, command := range closing {
				finish(command, response)
			}
			if stop {
				return nil
			}
			closing = nil
			deadline = nil
		case done := <-w.completed:
			if r := w.repos[done.path]; r != nil && r.version == done.version {
				r.running = false
				for id, task := range w.hypotheses {
					if task.request.Repository == done.path {
						delete(w.hypotheses, id)
					}
				}
				if w.cfg.RequireHypotheses && done.result.assessmentExpired && !r.retriedAssessment {
					if turnEnded && !r.pending {
						// Keep an ended turn scoped to the source it already captured.
						r.frozen = &done.capture
					} else if !turnEnded {
						// The replacement captures the latest source. Mark it observed
						// so the next scan does not queue that edit a second time.
						bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
						if fingerprint, err := workspaceFingerprint(bounded, done.path); err == nil {
							r.observed = fingerprint
						}
						cancel()
					}
					r.retriedAssessment = true
					r.pending = true
					w.schedule(ctx)
					continue
				}
				r.retriedAssessment = false
				r.result = &done.result
				if strings.Contains(done.result.Error, errAgentResponseTimeout.Error()) {
					turnEnded = true
					for _, pending := range w.repos {
						pending.pending = false
					}
				}
				if w.cfg.Report != nil {
					w.cfg.Report(done.result)
				}
			}
			w.schedule(ctx)
		case _, ok := <-w.watcher.Events:
			if !ok {
				return nil
			}
			if turnEnded || len(closing) > 0 {
				continue
			}
			if debounce == nil {
				debounce = time.NewTimer(750 * time.Millisecond)
			} else {
				debounce.Reset(750 * time.Millisecond)
			}
			settled = debounce.C
		case <-w.watcher.Errors:
			// The fallback scan detects changes even if notification delivery overflows.
		case <-settled:
			settled = nil
			if !turnEnded && len(closing) == 0 {
				if err := scan(); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if !turnEnded && len(closing) == 0 && settled == nil {
				if err := scan(); err != nil {
					return err
				}
			}
		}
	}
}

func (w *worker) prepareClaudeFindingHandoff(command control, response *Response) {
	if w.findingToolDenied {
		return
	}
	if command.finish {
		if w.findingStopOffered || len(response.HypothesisRequests) > 0 {
			return
		}
		for _, finding := range response.Findings {
			if finding.delivered && finding.Disposition == nil {
				response.FindingStopFollowup = true
				w.findingStopOffered = true
				break
			}
		}
		return
	}
	if command.checkpoint || command.listFindings {
		for _, finding := range response.Findings {
			if finding.Disposition == nil {
				response.FindingHandoff = true
				key := finding.ReviewID + "/" + finding.Lead.ID
				finding.delivered = true
				w.findings[key] = finding
			}
		}
	}
}

// addDirectory runs on the worker loop, so registration cannot race a scan.
// Existing repository state is retained, including pending/in-flight reviews.
func (w *worker) addDirectory(ctx context.Context, directory string) error {
	if !filepath.IsAbs(directory) {
		return fmt.Errorf("directory must be absolute")
	}
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	info, err := os.Stat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory")
	}
	if !w.allowed(ctx, "") {
		return fmt.Errorf("upload approval was revoked or the installation was removed")
	}
	for _, existing := range w.directories {
		if existing == directory {
			return nil
		}
	}
	repos, err := DiscoverRepositories(ctx, directory)
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		return fmt.Errorf("no Git repositories found in directory")
	}
	baselines := map[string]string{}
	for _, repo := range repos {
		if !w.allowed(ctx, repo) {
			return fmt.Errorf("upload permission is required for %s", repo)
		}
		if w.repos[repo] != nil {
			continue
		}
		fingerprint, err := workspaceFingerprint(ctx, repo)
		if err != nil {
			return err
		}
		baselines[repo] = fingerprint
	}
	if err := w.watchDirectories(ctx, directory); err != nil {
		return err
	}
	for repo, fingerprint := range baselines {
		baseline, err := initialEditFiles(ctx, repo)
		if err != nil {
			return err
		}
		w.repos[repo] = &repository{observed: fingerprint, baseline: baseline}
	}
	w.directories = append(w.directories, directory)
	return nil
}

// Offer each pending request once at Stop. Codex already sees checkpoint
// requests before answering. Claude can recover a truncated checkpoint at Stop.
func (w *worker) repeatStopHandoff() bool {
	repeated := true
	for _, pending := range w.hypotheses {
		if pending.stopOffered || pending.isExpired() {
			continue
		}
		pending.stopOffered = true
		if w.cfg.Agent == "codex" {
			select {
			case <-pending.delivered:
				continue
			default:
			}
		}
		repeated = false
	}
	return repeated
}

func (w *worker) assessmentsOfferedAtStop() bool {
	for _, pending := range w.hypotheses {
		if !pending.stopOffered && !pending.isExpired() {
			return false
		}
	}
	return true
}

func (w *worker) markHypothesesDelivered() {
	for _, p := range w.hypotheses {
		if p.delivered != nil {
			select {
			case <-p.delivered:
			default:
				close(p.delivered)
			}
		}
	}
}
