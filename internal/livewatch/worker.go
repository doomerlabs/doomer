package livewatch

import (
	"context"
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

	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"github.com/fsnotify/fsnotify"
)

type repository struct {
	observed         string
	pending, running bool
	version          int
	frozen           *sourcesnapshot.Capture
	result           *Result
	cancel           context.CancelFunc
}
type completion struct {
	path    string
	version int
	result  Result
}
type control struct {
	addDirectory string

	cancel         bool
	checkpoint     bool
	finish         bool
	results        bool
	noticeID       string
	noticeRevision int
	feedback       []Feedback
	reply          chan Response
	sent           chan struct{}
}
type worker struct {
	cfg           Config
	client        Client
	allowed       func(context.Context, string) bool
	directories   []string
	repos         map[string]*repository
	noticeAcks    map[string]bool
	findings      map[string]Finding
	watcher       *fsnotify.Watcher
	controls      chan control
	completed     chan completion
	slots         chan struct{}
	jobs          sync.WaitGroup
	closeListener func() error
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

func serve(ctx context.Context, listener net.Listener, cfg Config, client Client, allowed func(context.Context, string) bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &worker{directories: []string{cfg.Directory}, cfg: cfg, client: client, allowed: allowed, repos: map[string]*repository{}, findings: map[string]Finding{}, noticeAcks: map[string]bool{}, controls: make(chan control), completed: make(chan completion, 64), slots: make(chan struct{}, 4)}
	w.closeListener = func() error { return nil }
	if listener != nil {
		w.closeListener = listener.Close
	}
	defer func() { cancel(); w.jobs.Wait() }()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	w.watcher = watcher
	defer watcher.Close()
	startup, stopStartup := context.WithTimeout(ctx, 20*time.Second)
	err = w.scan(startup, true, false)
	stopStartup()
	if err != nil {
		return err
	}
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
					if json.NewDecoder(io.LimitReader(conn, 256<<10)).Decode(&req) != nil || req.Key != cfg.ControlKey {
						return
					}
					if req.Operation == "notice-ack" && (req.NoticeID == "" || req.NoticeRevision < 1) {
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
					if req.Operation != "add" && req.Operation != "stop" && req.Operation != "cancel" && req.Operation != "checkpoint" && req.Operation != "feedback" && req.Operation != "finish" && req.Operation != "results" && req.Operation != "notice-ack" {
						return
					}
					command := control{addDirectory: req.Directory, results: req.Operation == "results", cancel: req.Operation == "cancel", checkpoint: req.Operation == "checkpoint" || req.Operation == "finish", finish: req.Operation == "finish", feedback: req.Feedback, noticeID: req.NoticeID, noticeRevision: req.NoticeRevision, reply: make(chan Response, 1), sent: make(chan struct{})}
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
			r = &repository{observed: fingerprint, pending: !initial}
			w.repos[path] = r
		} else if fingerprint != r.observed {
			r.observed = fingerprint
			r.pending = true
		}
		if freeze && r.pending {
			capture, err := capture(ctx, path)
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

func capture(ctx context.Context, path string) (sourcesnapshot.Capture, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return sourcesnapshot.CaptureWorktree(ctx, sourcesnapshot.CaptureOptions{Workspace: true, Path: path})
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
		var err error
		if r.frozen != nil {
			snapshot = *r.frozen
			r.frozen = nil
		} else {
			snapshot, err = capture(ctx, path)
		}
		if err != nil {
			if !errors.Is(err, sourcesnapshot.ErrNoChanges) {
				r.result = &Result{Repository: path, Status: "unavailable", Error: err.Error()}
			}
			continue
		}
		r.version++
		r.running = true
		jobCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		r.cancel = cancel
		version := r.version
		w.jobs.Add(1)
		go func(path string, version int, snapshot sourcesnapshot.Capture) {
			defer w.jobs.Done()
			defer cancel()
			select {
			case w.slots <- struct{}{}:
			case <-jobCtx.Done():
				return
			}
			defer func() { <-w.slots }()
			result := review(jobCtx, w.client, w.cfg, path, snapshot, version)
			select {
			case w.completed <- completion{path, version, result}:
			case <-ctx.Done():
			}
		}(path, version, snapshot)
	}
}

func (w *worker) response(timeout bool) Response {
	response := Response{Ready: true, API: w.cfg.API, Directory: w.cfg.Directory}
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

func (w *worker) hasPendingFeedback() bool {
	for _, notice := range w.response(false).Notices {
		if notice.NotifyUser {
			return true
		}
	}

	for _, finding := range w.findings {
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
	scan := func() error {
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return w.scan(bounded, false, false)
	}
	ticker := time.NewTicker(time.Second)
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
	finish := func(command control, response Response) {
		command.reply <- response
		select {
		case <-command.sent:
		case <-time.After(time.Second):
		}
	}
	w.schedule(ctx)
	for {
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
			if !busy {
				w.collectFindings()
				response := w.response(false)
				stop := false
				for _, command := range closing {
					stop = stop || !command.checkpoint || (command.finish && !w.hasPendingFeedback())
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
				if drain != nil {
					drain.Stop()
				}
				deadline = nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case command := <-w.controls:

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
				known := false
				for _, n := range response.Notices {
					if n.ID == command.noticeID && n.Revision == command.noticeRevision {
						known = true
						break
					}
				}
				if !known || w.cfg.AcknowledgeNotice == nil {
					response.Error = "unknown advisory revision"
				} else if err := w.cfg.AcknowledgeNotice(command.noticeID, command.noticeRevision); err != nil {
					response.Error = err.Error()
				} else {
					w.noticeAcks[fmt.Sprintf("%s:%d", command.noticeID, command.noticeRevision)] = true
					response = w.response(false)
				}
				finish(command, response)
				continue
			}
			if command.results {
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
				finalCtx, stop := context.WithTimeout(ctx, timeout)
				err := w.scan(finalCtx, false, true)
				stop()
				if err != nil {
					finish(command, Response{Error: fmt.Sprintf("capture final changes: %v", err)})
					return err
				}
			}
			closing = append(closing, command)
			w.schedule(ctx)
		case <-deadline:
			w.collectFindings()
			response := w.response(true)
			stop := false
			for _, command := range closing {
				stop = stop || !command.checkpoint || (command.finish && !w.hasPendingFeedback())
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
				r.result = &done.result
				if w.cfg.Report != nil {
					w.cfg.Report(done.result)
				}
			}
			w.schedule(ctx)
		case _, ok := <-w.watcher.Events:
			if !ok {
				return nil
			}
			if len(closing) > 0 {
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
			if len(closing) == 0 {
				if err := scan(); err != nil {
					return err
				}
				w.schedule(ctx)
			}
		case <-ticker.C:
			if len(closing) == 0 && settled == nil {
				if err := scan(); err != nil {
					return err
				}
				w.schedule(ctx)
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
		w.repos[repo] = &repository{observed: fingerprint}
	}
	w.directories = append(w.directories, directory)
	return nil
}
