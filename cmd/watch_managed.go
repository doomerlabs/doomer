package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/publock"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

// The existing background lifecycle uses its own consent and process protocol.
// Keep it separate from the foreground watcher used by Node's direct hooks.
func newManagedWatchCommand(app *application.App, api, profile *string) *cobra.Command {
	var config, base string
	var debug bool
	var managedSession string
	var interval time.Duration
	c := &cobra.Command{Use: "managed [directories...]", Hidden: true, Short: "Run an existing managed background watcher", Args: cobra.ArbitraryArgs, RunE: func(c *cobra.Command, args []string) error {
		root, file, err := watchPaths(*api, *profile)
		if err != nil {
			return err
		}

		if interval < time.Second {
			return fmt.Errorf("--interval must be at least 1s")
		}
		if config != "" {
			if len(args) > 0 {
				return fmt.Errorf("use directories or --config, not both")
			}
			var selection struct {
				Directories []string `json:"directories"`
			}
			if err = watching.Read(config, &selection); err != nil {
				return err
			}
			if len(selection.Directories) == 0 {
				return fmt.Errorf("watch config has no directories")
			}
			args = selection.Directories
		}
		var cfg watching.Config
		var auth adversarylabs.Auth
		if managedSession != "" {
			auth, err = watchAuth(app, *api, *profile)
			if err == nil {
				err = watching.Read(file, &cfg)
			}
			if err == nil && !watching.ConsentValid(cfg, *api, *profile, auth.Token) {
				err = fmt.Errorf("upload approval is required before starting a managed watcher")
			}
		} else {
			cfg, auth, err = prepareWatch(c, app, api, profile, args)
		}
		if err != nil {
			return err
		}

		cfg.RuntimeVersion = activeRuntimeVersion()
		dirs := cfg.Directories
		if len(args) == 0 {
			dirs = initialWatchDirectories(c.Context(), cfg, ".")
		}
		if len(args) > 0 {
			dirs = nil
			for _, p := range args {
				canonical, e := watching.Root(c.Context(), p)
				if e != nil {
					return e
				}
				if !watching.Allowed(cfg, canonical) {
					return fmt.Errorf("directory not approved; run doomer watch add %s", canonical)
				}
				dirs = append(dirs, canonical)
			}
		}
		unique := []string{}
		seenDirs := map[string]bool{}
		for _, dir := range dirs {
			if !seenDirs[dir] {
				unique = append(unique, dir)
				seenDirs[dir] = true
			}
		}
		dirs = unique
		discover := len(args) == 0
		if !discover && len(dirs) == 0 {
			return fmt.Errorf("select at least one repository")
		}
		var retirementLock *publock.Lock
		defer func() { _ = retirementLock.Close() }()
		lock, err := publock.TryAcquire(root, "watcher")
		if err != nil {
			return fmt.Errorf("a watcher for this profile is already running")
		}
		defer lock.Close()
		if paused(root) {
			return fmt.Errorf("watching is paused; run doomer onboard --resume to resume explicitly")
		}
		watchStarted := time.Now()
		watchSession := watchStarted.UTC().Format(time.RFC3339Nano)
		if err := watching.Write(filepath.Join(root, "watch-session.json"), watchSession); err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(c.Context())
		defer cancel()
		heartbeat := filepath.Join(root, "heartbeat.json")
		_ = watching.Write(heartbeat, time.Now())
		defer os.Remove(heartbeat)
		workingDirectory, err := os.Getwd()
		if err != nil {
			return err
		}
		process := watchProcess{WorkingDirectory: workingDirectory, PID: os.Getpid(), Session: watchSession, ManagedSession: managedSession, ConsentedAt: cfg.ConsentedAt, Directories: args}
		if err := watching.Write(filepath.Join(root, "process.json"), process); err != nil {
			return err
		}
		defer os.Remove(filepath.Join(root, "process.json"))
		// Revocation stops all in-flight uploads, including during initial assessment.
		go func() {
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					_ = watching.Write(heartbeat, time.Now())
					var current watching.Config
					active, e := watchAuth(app, *api, *profile)
					if paused(root) || e != nil || watching.Read(file, &current) != nil || !watching.ConsentValid(current, *api, *profile, active.Token) || !current.ConsentedAt.Equal(cfg.ConsentedAt) {
						cancel()
						return
					}
				}
			}
		}()
		client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, *api)
		output := &watchOutput{w: c.OutOrStdout()}
		fmt.Fprintf(output, "Doomer watch · profile %s · %s\nPress Ctrl+C to stop.\n", *profile, *api)
		if adversarylabs.IsAnonymousToken(auth.Token) {
			fmt.Fprintln(output, "Anonymous mode: free Look Closer only. Source code is uploaded to the selected service.")
		}
		statusf := func(format string, args ...any) {
			fmt.Fprintf(output, "[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
		}
		debugf := func(format string, args ...any) {
			if debug {
				fmt.Fprintf(output, "[debug %s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
			}
		}
		debugf("filesystem events enabled; fallback scan interval=%s; discovery=%t; initial repositories=%d", interval, discover, len(dirs))
		var group sync.WaitGroup
		type runningWatch struct{ cancel context.CancelFunc }
		type finishedWatch struct {
			path string
			run  *runningWatch
			err  error
		}
		running := map[string]*runningWatch{}
		finished := make(chan finishedWatch)
		selected := map[string]bool{}
		for _, dir := range dirs {
			if !discover {
				selected[dir] = true
			}
			explicit := false
			for _, approved := range cfg.Directories {
				if approved == dir {
					explicit = true
					break
				}
			}
			if !watching.RepositoryRemoved(root, dir) {
				if e := watching.RegisterRepository(root, dir, "current checkout", explicit, watchStarted); e != nil {
					return e
				}
			}
		}
		launch := func(path string) {
			if running[path] != nil || watching.RepositoryRemoved(root, path) {
				return
			}
			runCtx, stop := context.WithCancel(ctx)
			run := &runningWatch{cancel: stop}
			running[path] = run
			statusf("Watching %s — waiting for agent edits", path)
			group.Add(1)
			go func() {
				defer group.Done()
				defer stop()
				token := time.Now().UTC().Format(time.RFC3339Nano)
				_ = watching.SetWatchRuntime(root, path, watching.WatchRuntime{Session: watchSession, Token: token, Status: "Starting"})
				defer watching.ClearWatchRuntime(root, path, token)
				r := watching.Runner{Root: root, Config: cfg, Token: auth.Token, Client: client, Output: output, Interval: interval, Base: base, Debug: debug, StartedAt: watchStarted, WatchSession: watchSession, WatchToken: token}
				e := r.Run(runCtx, path, false)
				watching.ClearWatchRuntime(root, path, token)
				select {
				case finished <- finishedWatch{path, run, e}:
				case <-ctx.Done():
				}
			}()
		}
		if discover {
			statusf("Agent discovery enabled; automatic checkouts become inactive after 14 days without agent activity.")
		}
		lastWorktreeScan := time.Time{}
		nextPromptCleanup := time.Time{}
		idleSince := time.Now()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for ctx.Err() == nil {
			if time.Now().After(nextPromptCleanup) {
				watching.PrunePendingPrompts(root, time.Now())
				nextPromptCleanup = time.Now().Add(time.Minute)
			}
			repos, e := watching.Repositories(root, cfg)
			if e != nil {
				cancel()
				group.Wait()
				return e
			}
			wanted := map[string]bool{}
			for _, repo := range repos {
				if repo.Removed || !watching.Allowed(cfg, repo.Path) {
					continue
				}
				if !discover && !selected[repo.Path] {
					continue
				}
				if repo.Active(time.Now()) || selected[repo.Path] {
					if running[repo.Path] != nil {
						wanted[repo.Path] = true
						continue
					}
					canonical, e := watching.Root(ctx, repo.Path)
					if e == nil && canonical == repo.Path {
						wanted[repo.Path] = true
						launch(repo.Path)
					}
				}
			}
			for path, run := range running {
				if !wanted[path] {
					run.cancel()
					delete(running, path)
					statusf("Stopped watching %s — inactive or removed.", path)
				}
			}
			if len(wanted) > 0 {
				idleSince = time.Now()
			} else if managedSession != "" && time.Since(idleSince) >= time.Minute {
				// Serialize retirement with hook startup. The hook registers its
				// checkout under this same lock before checking watcher liveness.
				lock, e := lifecycleLock(ctx, root)
				if e == nil {
					active, e := activeRuntimeRepositories(root, cfg, time.Now())
					if e == nil && !active {
						retirementLock = lock
						cancel()
						group.Wait()
						statusf("No active checkouts use this runtime; stopping the background watcher.")
						return nil
					}
					lock.Close()
				}
			}
			if time.Since(lastWorktreeScan) >= 5*time.Second {
				roots := make([]string, 0, len(running))
				for path := range running {
					roots = append(roots, path)
				}
				scanCtx, stop := context.WithTimeout(ctx, 3*time.Second)
				linked := watching.LinkedWorktrees(scanCtx, cfg, roots)
				stop()
				for _, path := range linked {
					// Knowing a Git worktree exists does not activate its file watchers.
					if e := watching.RegisterRepository(root, path, "linked worktree", false, time.Time{}); e != nil {
						debugf("could not register linked checkout: %s", e)
					}
				}
				lastWorktreeScan = time.Now()
			}
			select {
			case <-ctx.Done():
			case result := <-finished:
				if running[result.path] == result.run {
					delete(running, result.path)
				}
				if result.err != nil {
					statusf("%s: watcher stopped: %s", result.path, result.err)
				}
			case <-ticker.C:
			}
		}
		group.Wait()

		return nil
	}}
	c.Flags().StringVar(&managedSession, "managed-session", "", "internal background startup identity")
	c.Flags().BoolVar(&debug, "debug", false, "log background activity")
	c.Flags().StringVar(&config, "config", "", "approved directory selection")
	c.Flags().StringVar(&base, "base", "", "local baseline ref")
	c.Flags().DurationVar(&interval, "interval", time.Minute, "fallback scan interval")
	return c
}
