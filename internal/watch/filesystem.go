package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

const filesystemDebounce = 750 * time.Millisecond

// Directory watches survive atomic file replacement. Notifications only wake
// capture; the stable snapshot and agent ownership remain the upload authority.
type filesystemEvents struct {
	watcher                 *fsnotify.Watcher
	changed                 chan struct{}
	path, stateRoot, gitDir string
	lastRequest             time.Time
	lastHypotheses          bool
	lastActive              int
	refreshedAt             time.Time
	runner                  Runner
}

func newFilesystemEvents(ctx context.Context, r Runner, path string) (*filesystemEvents, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	events := &filesystemEvents{watcher: watcher, changed: make(chan struct{}, 1), path: path, stateRoot: r.Root, runner: r, refreshedAt: time.Now()}
	if err = events.addTree(ctx, path); err != nil {
		watcher.Close()
		return nil, err
	}
	// Git metadata may live outside a linked worktree.
	cmd := filesystemGit(ctx, path, "rev-parse", "--absolute-git-dir")
	if raw, e := cmd.Output(); e == nil {
		events.gitDir = strings.TrimSpace(string(raw))
		if err = watcher.Add(events.gitDir); err != nil {
			watcher.Close()
			return nil, err
		}
	}
	// Hook state and response queues also wake a sleeping watcher.
	for _, dir := range []string{r.Root, filepath.Join(r.Root, "repositories"), filepath.Join(r.Root, "feedback"), filepath.Join(r.Root, "hook-diagnostics")} {
		if err = os.MkdirAll(dir, 0700); err == nil {
			err = watcher.Add(dir)
		}
		if err != nil {
			watcher.Close()
			return nil, err
		}
	}
	var state State
	_ = Read(StatePath(r.Root, path), &state)
	events.lastRequest = state.RequestedAt
	events.lastHypotheses = state.HypothesesSubmitted
	events.lastActive = len(state.Active)
	go events.run(ctx)
	return events, nil
}

func (e *filesystemEvents) ignored(ctx context.Context, path string) bool {
	if filepath.Base(path) == ".git" {
		return true
	}
	cmd := filesystemGit(ctx, e.path, "check-ignore", "-q", "--", path)
	// Exit 1 means not ignored. Other failures conservatively keep the path.
	return cmd.Run() == nil
}
func (e *filesystemEvents) addTree(ctx context.Context, root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !entry.IsDir() {
			return nil
		}
		if path != e.path && e.ignored(ctx, path) {
			return filepath.SkipDir
		}
		return e.watcher.Add(path)
	})
}
func (e *filesystemEvents) signal() {
	select {
	case e.changed <- struct{}{}:
	default:
	}
}
func (e *filesystemEvents) run(ctx context.Context) {
	var lastDiagnostic time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-e.watcher.Errors:
			if !ok {
				return
			}
			e.runner.logf("%s: filesystem notification missed (%s); rescanning.", filepath.Base(e.path), err)
			e.signal()
		case event, ok := <-e.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			parent := filepath.Dir(event.Name)
			if parent == filepath.Join(e.stateRoot, "hook-diagnostics") {
				if e.runner.Debug {
					lastDiagnostic = e.runner.logHookDiagnostics(e.path, lastDiagnostic)
				}
				continue
			}
			if parent == e.stateRoot { // Heartbeats are not repository changes.
				if event.Has(fsnotify.Create) && (filepath.Base(event.Name) == "repositories" || filepath.Base(event.Name) == "feedback" || filepath.Base(event.Name) == "hook-diagnostics") {
					_ = e.watcher.Add(event.Name)
				}
				continue
			}
			if parent == filepath.Join(e.stateRoot, "repositories") {
				if filepath.Base(event.Name) == Key(e.path)+".json" {
					var state State
					if Read(event.Name, &state) == nil {
						latest := state.RequestedAt
						for _, op := range state.Active {
							if op.CompletedAt.After(latest) {
								latest = op.CompletedAt
							}
						}
						if latest.After(e.lastRequest) || state.HypothesesSubmitted != e.lastHypotheses || (e.lastActive > 0 && len(state.Active) == 0 && state.RequestedAt.After(e.runner.StartedAt)) {
							e.signal()
						}
						e.lastRequest = latest
						e.lastHypotheses = state.HypothesesSubmitted
						e.lastActive = len(state.Active)
					}
				}
				continue
			}
			if parent == filepath.Join(e.stateRoot, "feedback") {
				e.signal()
				continue
			}
			if parent == e.gitDir {
				if filepath.Base(event.Name) == "HEAD" || filepath.Base(event.Name) == "index" {
					e.signal()
				}
				continue
			}
			if e.ignored(ctx, event.Name) {
				continue
			}
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					if err = e.addTree(ctx, event.Name); err != nil {
						e.runner.logf("%s: could not watch new directory; periodic rescans remain enabled.", filepath.Base(e.path))
					}
				}
			}
			e.signal()
		}
	}
}

// Normal idle waits do no filesystem scans. Retry deadlines and a slow fallback
// still wake capture when a filesystem does not deliver reliable notifications.
func (e *filesystemEvents) wait(ctx context.Context, fallback, retry time.Duration) bool {
	if fallback <= 0 {
		fallback = time.Minute
	}
	if retry > 0 && retry < fallback {
		fallback = retry
	}
	timer := time.NewTimer(fallback)
	defer timer.Stop()
	var changed <-chan struct{}
	if e != nil {
		changed = e.changed
	}
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-changed:
	}
	quiet := time.NewTimer(filesystemDebounce)
	defer quiet.Stop()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-quiet.C:
			return true
		case <-deadline.C:
			return true
		case <-changed:
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(filesystemDebounce)
		}
	}
}

func filesystemGit(ctx context.Context, path string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0")
	return cmd
}
