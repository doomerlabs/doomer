package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/publock"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

type watchProcess struct {
	PID            int       `json:"pid"`
	Session        string    `json:"session"`
	ManagedSession string    `json:"managed_session,omitempty"`
	ConsentedAt    time.Time `json:"consented_at"`
	Directories    []string  `json:"directories,omitempty"`
}

type watchStatus struct {
	API              string          `json:"api_url"`
	Profile          string          `json:"profile"`
	Repository       string          `json:"repository"`
	Approved         bool            `json:"approved"`
	Paused           bool            `json:"paused"`
	HooksInstalled   map[string]bool `json:"hooks_installed"`
	HookTrust        string          `json:"hook_trust"`
	Running          bool            `json:"running"`
	RepositoryStatus string          `json:"repository_status"`
	Log              string          `json:"log"`
}

func paused(root string) bool {
	_, err := os.Stat(filepath.Join(root, "paused.json"))
	// Fail closed if the pause marker cannot be inspected.
	return !os.IsNotExist(err)
}

// A fresh heartbeat alone is not evidence of a live process after a crash.
// The watcher holds this OS lock for its entire lifetime; it is never PID-killed.
func runningWatch(root string) (watchProcess, bool, error) {
	var p watchProcess
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return p, false, nil
	} else if err != nil {
		return p, false, err
	}
	lock, err := publock.TryAcquire(root, "watcher")
	if err == nil {
		return p, false, lock.Close()
	}
	if !publock.Busy(err) {
		return p, false, err
	}
	var heartbeat time.Time
	if e := watching.Read(filepath.Join(root, "process.json"), &p); e != nil {
		return p, false, fmt.Errorf("watcher is starting or unavailable: %w", e)
	}
	if e := watching.Read(filepath.Join(root, "heartbeat.json"), &heartbeat); e != nil {
		return p, false, fmt.Errorf("watcher heartbeat unavailable: %w", e)
	}
	if age := time.Since(heartbeat); age < 0 || age > 5*time.Second {
		return p, false, fmt.Errorf("watcher is not responding; inspect %s", filepath.Join(root, "watch.log"))
	}
	return p, true, nil
}

func waitWatch(ctx context.Context, check func() (bool, error)) error {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		done, err := check()
		if done && err == nil {
			return nil
		}
		if err != nil {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if last != nil {
				return last
			}
			return fmt.Errorf("timed out waiting for watcher")
		case <-ticker.C:
		}
	}
}

func lifecycleLock(ctx context.Context, root string) (*publock.Lock, error) {
	var lock *publock.Lock
	err := waitWatch(ctx, func() (bool, error) {
		var err error
		lock, err = publock.TryAcquire(root, "watch-lifecycle")
		return err == nil, err
	})
	return lock, err
}

// Status reads credentials without creating an anonymous identity or contacting a server.
func watchStatusAuth(app *application.App, api, profile, root string) (adversarylabs.Auth, error) {
	auth, ok, err := scopedAuth(app.Dependencies().Auth, api, profile, app.Dependencies().RegistryHost)
	if err != nil || ok && auth.Token != "" {
		return auth, err
	}
	var saved struct{ API, Profile, Token string }
	if err := watching.Read(filepath.Join(root, "anonymous.json"), &saved); os.IsNotExist(err) {
		return adversarylabs.Auth{}, nil
	} else if err != nil {
		return auth, err
	}
	if saved.API != api || saved.Profile != profile || !adversarylabs.IsAnonymousToken(saved.Token) {
		return auth, fmt.Errorf("invalid anonymous review state")
	}
	auth.Token = saved.Token
	return auth, nil
}

func inspectWatch(app *application.App, api, profile, path string) (watchStatus, error) {
	s := watchStatus{API: api, Profile: profile, Repository: path, HooksInstalled: map[string]bool{}, HookTrust: "user_managed", RepositoryStatus: "Not running"}
	root, file, err := watchPaths(api, profile)
	if err != nil {
		return s, err
	}
	s.Log = filepath.Join(root, "watch.log")
	var cfg watching.Config
	if err = watching.Read(file, &cfg); err != nil && !os.IsNotExist(err) {
		return s, err
	}
	auth, err := watchStatusAuth(app, api, profile, root)
	if err != nil {
		return s, err
	}
	s.Approved = watching.ConsentValid(cfg, api, profile, auth.Token) && watching.Allowed(cfg, path)
	s.Paused = paused(root) || watching.RepositoryRemoved(root, path)
	home, err := os.UserHomeDir()
	if err != nil {
		return s, err
	}
	command, err := watchHookCommand(api, profile, root)
	if err != nil {
		return s, err
	}
	for _, agent := range []string{"codex", "claude"} {
		installed, err := watching.Installed(watching.HookFile(home, agent), command+" --agent "+watching.Quote(agent))
		if err != nil {
			return s, err
		}
		s.HooksInstalled[agent] = installed
	}
	_, s.Running, err = runningWatch(root)
	if err != nil {
		return s, err
	}
	if s.Approved && !s.Paused && s.Running {
		repos, err := watching.Repositories(root, cfg)
		if err != nil {
			return s, err
		}
		for _, repo := range repos {
			if repo.Path == path {
				s.RepositoryStatus = watching.RepositoryStatus(root, repo, time.Now())
			}
		}
	}
	return s, nil
}

func addWatchLifecycleCommands(parent *cobra.Command, app *application.App, api, profile *string) {
	var resume bool
	start := &cobra.Command{Use: "start [directory]", Short: "Approve this repository and start or reuse a background watcher", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}
		path, err := watching.Root(c.Context(), path)
		if err != nil {
			return err
		}
		root, _, err := watchPaths(*api, *profile)
		if err != nil {
			return err
		}
		pauseBefore, _ := os.ReadFile(filepath.Join(root, "paused.json"))
		if (paused(root) || watching.RepositoryRemoved(root, path)) && !resume {
			return fmt.Errorf("watching is paused; use watch start --resume to explicitly resume")
		}
		cfg, _, err := prepareWatch(c, app, api, profile, []string{path})
		if err != nil {
			return err
		}
		lock, err := lifecycleLock(c.Context(), root)
		if err != nil {
			return err
		}
		defer lock.Close()
		pauseAfter, _ := os.ReadFile(filepath.Join(root, "paused.json"))
		if !bytes.Equal(pauseBefore, pauseAfter) || paused(root) && !resume {
			return fmt.Errorf("watching was paused during setup; use watch start --resume when ready")
		}
		if resume {
			if err := os.Remove(filepath.Join(root, "paused.json")); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := watching.SetRepositorySelection(root, path, false); err != nil {
				return err
			}
		}
		if err := watching.RegisterRepository(root, path, "explicit", true, time.Time{}); err != nil {
			return err
		}
		var current watchProcess
		var running bool
		// Changing consent makes the previous watcher exit before the new one starts.
		err = waitWatch(c.Context(), func() (bool, error) {
			var err error
			current, running, err = runningWatch(root)
			return err == nil && (!running || current.ConsentedAt.Equal(cfg.ConsentedAt)), err
		})
		if err != nil {
			return err
		}
		if running {
			if len(current.Directories) != 0 && !containsPath(current.Directories, path) {
				return fmt.Errorf("the foreground watcher does not cover this repository; stop it before using watch start")
			}
			fmt.Fprintln(c.OutOrStdout(), "Reusing the running watcher.")
		} else {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			session := rand.Text()
			child := exec.Command(exe, "--api-url", *api, "--profile", *profile, "watch", "--managed-session", session)
			child.Dir = path
			// Resolve an explicitly relative config directory before changing
			// the child's working directory to another approved repository.
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "DOOMER_CONFIG_DIR=") {
					child.Env = append(child.Env, entry)
				}
			}
			child.Env = append(child.Env, "DOOMER_CONFIG_DIR="+filepath.Dir(filepath.Dir(root)))
			if err := startWatchProcess(child, filepath.Join(root, "watch.log")); err != nil {
				return err
			}
			err = waitWatch(c.Context(), func() (bool, error) {
				p, live, err := runningWatch(root)
				return live && p.ManagedSession == session, err
			})
			if err != nil {
				return fmt.Errorf("background watcher did not become ready: %w; inspect %s", err, filepath.Join(root, "watch.log"))
			}
			fmt.Fprintln(c.OutOrStdout(), "Background watcher started.")
		}
		fmt.Fprintf(c.OutOrStdout(), "Repository: %s\nService: %s\nLog: %s\nCodex hook trust remains a user action in /hooks.\n", path, *api, filepath.Join(root, "watch.log"))
		return nil
	}}
	addWatchSetupFlags(start)
	start.Flags().BoolVar(&resume, "resume", false, "explicitly resume a paused watcher or repository")
	var jsonOutput bool
	status := &cobra.Command{Use: "status [directory]", Short: "Inspect repository consent, hooks, and watcher readiness", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}
		path, err := watching.Root(c.Context(), path)
		if err != nil {
			return err
		}
		s, err := inspectWatch(app, *api, *profile, path)
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(c.OutOrStdout()).Encode(s)
		}
		fmt.Fprintf(c.OutOrStdout(), "Repository: %s\nService: %s\nUpload approved: %t\nPaused: %t\nWatcher running: %t\nRepository status: %s\nCodex hooks installed: %t\nClaude hooks installed: %t\nHook trust: check /hooks in Codex\nLog: %s\n", s.Repository, s.API, s.Approved, s.Paused, s.Running, s.RepositoryStatus, s.HooksInstalled["codex"], s.HooksInstalled["claude"], s.Log)
		return nil
	}}
	status.Flags().BoolVar(&jsonOutput, "json", false, "emit structured readiness without changing setup")
	stop := &cobra.Command{Use: "stop", Short: "Pause this profile's watcher without revoking upload permission", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		root, _, err := watchPaths(*api, *profile)
		if err != nil {
			return err
		}
		lock, err := lifecycleLock(c.Context(), root)
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := watching.Write(filepath.Join(root, "paused.json"), time.Now()); err != nil {
			return err
		}
		if err := waitWatch(c.Context(), func() (bool, error) { _, live, err := runningWatch(root); return !live, err }); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "Watching paused for this profile. Use watch start --resume to resume.")
		return nil
	}}
	revoke := &cobra.Command{Use: "revoke [directory]", Short: "Revoke this repository's upload permission", Args: cobra.MaximumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		path := "."
		if len(args) > 0 {
			path = args[0]
		}
		path, err := watching.Root(c.Context(), path)
		if err != nil {
			return err
		}
		root, file, err := watchPaths(*api, *profile)
		if err != nil {
			return err
		}
		lock, err := lifecycleLock(c.Context(), root)
		if err != nil {
			return err
		}
		defer lock.Close()
		var cfg watching.Config
		if err := watching.Read(file, &cfg); err != nil {
			return err
		}
		if cfg.AllRepositories {
			return fmt.Errorf("this legacy profile approved all repositories; use watch uninstall to revoke that approval")
		}
		id, err := watching.RepositoryIdentity(c.Context(), path)
		if err != nil {
			return err
		}
		var dirs, ids []string
		for _, dir := range cfg.Directories {
			dirID, err := watching.RepositoryIdentity(c.Context(), dir)
			if err != nil && dir != path {
				return err
			}
			if dir != path && dirID != id {
				dirs = append(dirs, dir)
			}
		}
		for _, approvedID := range cfg.RepositoryIDs {
			if approvedID != id {
				ids = append(ids, approvedID)
			}
		}
		cfg.Directories, cfg.RepositoryIDs, cfg.ConsentedAt = dirs, ids, time.Now()
		if err := watching.Write(file, cfg); err != nil {
			return err
		}
		if err := watching.SetRepositorySelection(root, path, true); err != nil {
			return err
		}
		if err := waitWatch(c.Context(), func() (bool, error) { _, live, err := runningWatch(root); return !live, err }); err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "Upload permission revoked for %s and its linked worktrees. The profile's watcher has stopped; use watch start in any remaining approved repository to restart it.\n", path)
		return nil
	}}
	parent.AddCommand(start, status, stop, revoke)
}

func containsPath(paths []string, path string) bool {
	for _, p := range paths {
		if p == path {
			return true
		}
	}
	return false
}
