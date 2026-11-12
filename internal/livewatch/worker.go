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
	cancel bool
	reply  chan Response
	sent   chan struct{}
}
type worker struct {
	cfg           Config
	client        Client
	allowed       func(context.Context, string) bool
	repos         map[string]*repository
	direct        string
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
	w := &worker{cfg: cfg, client: client, allowed: allowed, repos: map[string]*repository{}, controls: make(chan control), completed: make(chan completion, 64), slots: make(chan struct{}, 4)}
	w.closeListener = func() error { return nil }
	if listener != nil {
		w.closeListener = listener.Close
	}
	defer func() { cancel(); w.jobs.Wait() }()
	w.direct, _ = Root(ctx, cfg.Directory)
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
					if json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&req) != nil || req.Key != cfg.ControlKey {
						return
					}
					if req.Operation == "status" {
						_ = json.NewEncoder(conn).Encode(Response{Ready: true, API: cfg.API, Directory: cfg.Directory})
						return
					}
					if req.Operation != "stop" && req.Operation != "cancel" {
						return
					}
					command := control{cancel: req.Operation == "cancel", reply: make(chan Response, 1), sent: make(chan struct{})}
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

func (w *worker) watchDirectories(ctx context.Context) error {
	return filepath.WalkDir(w.cfg.Directory, func(path string, entry os.DirEntry, err error) error {
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
		if path != w.cfg.Directory {
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
	if !freeze {
		if err := w.watchDirectories(ctx); err != nil {
			return err
		}
	}
	paths, err := DiscoverRepositories(ctx, w.cfg.Directory)
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
			r = &repository{observed: fingerprint, pending: !initial || path == w.direct}
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
	for path, r := range w.repos {
		if timeout && (r.running || r.pending) {
			response.Results = append(response.Results, Result{Repository: path, Status: "unavailable", Error: "review did not finish before watch stopped"})
		} else if r.result != nil {
			response.Results = append(response.Results, *r.result)
		}
	}
	sort.Slice(response.Results, func(i, j int) bool { return response.Results[i].Repository < response.Results[j].Repository })
	return response
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
		_ = w.closeListener()
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
			if !busy {
				response := w.response(false)
				for _, command := range closing {
					finish(command, response)
				}
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case command := <-w.controls:
			if command.cancel {
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
			response := w.response(true)
			for _, command := range closing {
				finish(command, response)
			}
			return nil
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
