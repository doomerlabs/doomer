package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/paths"
	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var activeRuntimeVersion = runtimeversion.Current

func consentPaths(api, profile string) (string, string, error) {
	dir := os.Getenv("DOOMER_CONFIG_DIR")
	if dir == "" {
		var err error
		dir, err = paths.ConfigDir()
		if err != nil {
			return "", "", err
		}
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	root := filepath.Join(absolute, "watch", watching.Key(api+"\x00"+profile))
	return root, filepath.Join(root, "config.json"), nil
}

func watchPaths(api, profile string) (string, string, error) {
	root, file, err := consentPaths(api, profile)
	if err == nil && activeRuntimeVersion() != "" {
		root = filepath.Join(root, "runtimes", activeRuntimeVersion())
	}
	return root, file, err
}

func readWatchConfig(api, profile string, cfg *watching.Config) error {
	_, file, err := consentPaths(api, profile)
	if err != nil {
		return err
	}
	return watching.Read(file, cfg)
}

// Keep hook repair and readiness checks tied to this executable and destination.
func watchHookCommand(api, profile, root string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dispatch := ""
	if activeRuntimeVersion() != "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		exe = filepath.Join(home, ".local", "bin", "doomer")
		root, _, err = consentPaths(api, profile)
		if err != nil {
			return "", err
		}
		dispatch = " --dispatch"
	}
	prefix := ""
	if dir := os.Getenv("DOOMER_CONFIG_DIR"); dir != "" {
		absolute, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		prefix = "env DOOMER_CONFIG_DIR=" + watching.Quote(absolute) + " "
	}
	return prefix + watching.Quote(exe) + " --profile " + watching.Quote(profile) + " --api-url " + watching.Quote(api) + " watch hook --state " + watching.Quote(root) + dispatch, nil
}

func watchAuth(app *application.App, api, profile string) (adversarylabs.Auth, error) {
	return reviewAuth(app, api, profile)
}
func newSetupCommand(app *application.App, api, profile *string) *cobra.Command {
	var agents string
	var uninstall, status, yes, browser, noBrowser bool
	c := &cobra.Command{Use: "setup [directories...]", Short: "Approve repository uploads and install coding-agent hooks", Args: cobra.ArbitraryArgs, RunE: func(c *cobra.Command, args []string) error {
		root, file, err := watchPaths(*api, *profile)
		if err != nil {
			return err
		}
		var cfg watching.Config
		initialReadErr := watching.Read(file, &cfg)
		if initialReadErr != nil && !os.IsNotExist(initialReadErr) {
			return fmt.Errorf("invalid watch configuration: %w", initialReadErr)
		}
		initialConfig := cfg
		if status {
			fmt.Fprintf(c.OutOrStdout(), "Profile: %s\nService: %s\n", *profile, *api)
			if cfg.AllRepositories {
				fmt.Fprintln(c.OutOrStdout(), "Upload approval: automatic discovery of repositories your coding agent edits.")
			}
			for _, p := range cfg.Directories {
				fmt.Fprintln(c.OutOrStdout(), p)
			}
			fmt.Fprintln(c.OutOrStdout(), hookTrustInstructions(agents)+" Use watch status for readiness and watch start to run in the background.")
			return nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if agents != "codex" && agents != "claude" && agents != "both" {
			return fmt.Errorf("--agent must be codex, claude, or both")
		}
		selected := []string{agents}
		if agents == "both" {
			selected = []string{"codex", "claude"}
		}
		consentRoot, _, err := consentPaths(*api, *profile)
		if err != nil {
			return err
		}
		marker := "watch hook --state " + watching.Quote(consentRoot)
		if uninstall {
			lock, err := lifecycleLock(c.Context(), root)
			if err != nil {
				return err
			}
			defer lock.Close()
			if !c.Flags().Changed("agent") {
				selected = []string{"codex", "claude"}
			}
			for _, agent := range selected {
				if err = watching.Install(watching.HookFile(home, agent), "", marker, true); err != nil {
					return err
				}
			}
			if err = os.Remove(file); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), "Doomer CLI hooks removed and upload consent revoked for this profile.")
			return nil
		}
		if runtime.GOOS == "windows" {
			return fmt.Errorf("hook setup currently supports macOS and Linux")
		}
		auth, err := watchAuth(app, *api, *profile)
		if err != nil {
			return err
		}
		if !watching.ConsentValid(cfg, *api, *profile, auth.Token) {
			cfg = watching.Config{}
		}
		if len(args) == 0 {
			args = []string{"."}
		}
		allRepositories := false
		directories := []string{}
		seen := map[string]bool{}
		requested := args
		if !allRepositories && !cfg.AllRepositories {
			requested = append(cfg.Directories, args...)
		}
		for _, p := range requested {
			canonical, err := watching.Root(c.Context(), p)
			if err != nil {
				return err
			}
			if !seen[canonical] {
				directories = append(directories, canonical)
				seen[canonical] = true
			}
		}
		repositories, err := setupRepositories(c.Context(), directories, cfg)
		if err != nil {
			return err
		}
		reportActivation := yes
		consent := setupConsent{ActivationReporting: &reportActivation, Profile: *profile, API: *api, AllRepositories: allRepositories, Repositories: repositories, Agents: selected}
		f, ok := c.InOrStdin().(*os.File)
		interactive := ok && term.IsTerminal(int(f.Fd()))
		anonymous := adversarylabs.IsAnonymousToken(auth.Token)
		approved := yes
		var browserToken *adversarylabs.TokenResponse
		savedBefore, savedExisted, err := app.Dependencies().Auth.StoredAuthE(adversarylabs.AuthKey(*api, *profile))
		if err != nil {
			return err
		}
		if !yes && !interactive && !browser && !defaultSetupBrowser() {
			return fmt.Errorf("setup requires approval: use --browser for a human decision, or run in an interactive terminal; agents must not supply --yes")
		}
		if !yes && !noBrowser && (browser || defaultSetupBrowser()) {
			approved, browserToken, err = hostedSetupConsent(c.Context(), consent, auth.Token, adversarylabs.Client{BaseURL: *api}, c.OutOrStdout(), openSetupBrowser)
			if err != nil && !errors.Is(err, errBrowserLaunch) {
				return err
			}
			if errors.Is(err, errBrowserLaunch) {
				fmt.Fprintln(c.OutOrStdout(), "Browser setup is unavailable. Continue in this terminal.")
			} else if !approved {
				return fmt.Errorf("setup cancelled; no upload consent saved")
			}
		}
		if !approved {
			if !interactive {
				return fmt.Errorf("setup requires approval in an interactive terminal; no upload consent saved")
			}
			consent.print(c.OutOrStdout())
			fmt.Fprint(c.OutOrStdout(), "\nInstall hooks and allow these uploads? Type yes to continue: ")
			if !terminalSetupConsent(c.Context(), f) {
				return fmt.Errorf("setup cancelled; no upload consent saved")
			}
			reportActivation = true
		} else if anonymous && browserToken == nil {
			fmt.Fprintln(c.OutOrStdout(), "Free anonymous Look Closer enabled. Source code and agent review responses are uploaded to "+*api+". Deep review requires signing in.")
		} else if yes {
			consent.print(c.OutOrStdout())
			fmt.Fprintln(c.OutOrStdout(), "\nApproved with --yes.")
		}
		// A login replacement during browser approval must not inherit the old approval.
		currentAuth, err := watchAuth(app, *api, *profile)
		if err != nil {
			return err
		}
		if currentAuth.Token != auth.Token {
			return fmt.Errorf("login changed during setup; run setup again")
		}

		if c.Context().Err() != nil {
			return fmt.Errorf("setup cancelled; no upload consent saved")
		}
		// A human may revoke or replace approval while the browser is open.
		// Serialize the short commit phase and refuse to restore stale consent.
		approvalLock, err := lifecycleLock(c.Context(), consentRoot)
		if err != nil {
			return err
		}
		defer approvalLock.Close()
		var latest watching.Config
		latestErr := watching.Read(file, &latest)
		if latestErr != nil && !os.IsNotExist(latestErr) {
			return latestErr
		}
		if (initialReadErr == nil) != (latestErr == nil) || !latest.ConsentedAt.Equal(initialConfig.ConsentedAt) || latest.AccountKey != initialConfig.AccountKey {
			return fmt.Errorf("upload approval changed during setup; run setup again")
		}
		if browserToken != nil {
			saved := adversarylabs.Auth{Token: browserToken.Token, ClientID: browserToken.ClientID, ExpiresAt: browserToken.ExpiresAt, RegistryNamespace: browserToken.RegistryNamespace, Namespace: browserToken.Namespace, Team: browserToken.Team, RegistryHost: app.Dependencies().RegistryHost}
			if err := app.Dependencies().Auth.SetAuthCAS(adversarylabs.AuthKey(*api, *profile), savedBefore, savedExisted, saved); err != nil {
				return fmt.Errorf("save setup login: %w", err)
			}
			auth = saved
			fmt.Fprintf(c.OutOrStdout(), "Signed in to Doomer using profile %s.\n", *profile)
		}
		installationID, err := watching.EnsureInstallationID(consentRoot)
		if err != nil {
			return err
		}
		cfg = watching.Config{ActivationReporting: reportActivation, InstallationID: installationID, ExplicitConsent: true, AllRepositories: allRepositories, Version: 1, API: *api, Profile: *profile, Directories: directories, ConsentedAt: time.Now(), AccountKey: watching.Key(auth.Token)}
		for _, path := range directories {
			id, e := watching.RepositoryIdentity(c.Context(), path)
			if e != nil {
				return e
			}
			cfg.RepositoryIDs = append(cfg.RepositoryIDs, id)
		}
		command, err := watchHookCommand(*api, *profile, root)
		if err != nil {
			return err
		}
		if err = watching.Write(file, cfg); err != nil {
			return err
		}
		reportingConfig := cfg
		reportingConfig.RuntimeVersion = activeRuntimeVersion()
		for _, path := range directories {
			for _, agent := range selected {
				watching.QueueApproval(c.Context(), root, path, agent, reportingConfig)
			}
		}
		for _, agent := range selected {
			if err = watching.Install(watching.HookFile(home, agent), command+" --agent "+watching.Quote(agent)+" 2>/dev/null || printf '{}'", marker, false); err != nil {
				return err
			}
		}
		fmt.Fprintln(c.OutOrStdout(), "Integration saved. "+hookTrustInstructions(agents)+" Use watch start to run in the background. Use watch ls to manage watched checkouts or watch uninstall to remove the integration.")
		return nil
	}}
	c.Flags().BoolVar(&yes, "yes", false, "approve uploads and hook installation for the selected profile without prompting")
	c.Flags().BoolVar(&browser, "browser", false, "open browser setup for a human decision (also works from an agent)")
	c.Flags().BoolVar(&noBrowser, "no-browser", false, "approve setup in the terminal instead of a browser")
	c.MarkFlagsMutuallyExclusive("yes", "browser", "no-browser")
	c.Flags().StringVar(&agents, "agent", "codex", "install codex, claude, or both integrations")
	c.Flags().BoolVar(&uninstall, "uninstall", false, "remove this profile's CLI hooks and revoke upload consent")
	c.Flags().BoolVar(&status, "status", false, "show configured repositories and setup status")
	c.MarkFlagsMutuallyExclusive("status", "uninstall", "yes", "browser", "no-browser")
	return c
}

type watchOutput struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *watchOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// Include the current checkout without expanding this profile's approved scope.
func initialWatchDirectories(ctx context.Context, cfg watching.Config, cwd string) []string {
	dirs := append([]string(nil), cfg.Directories...)
	path, err := watching.Root(ctx, cwd)
	if err != nil || !watching.Allowed(cfg, path) {
		return dirs
	}
	for _, existing := range dirs {
		if existing == path {
			return dirs
		}
	}
	return append(dirs, path)
}

func newWatchCommand(app *application.App, api, profile *string) *cobra.Command {
	var config, base string
	var debug bool
	var managedSession string
	var interval time.Duration
	c := &cobra.Command{Use: "watch [directories...]", Short: "Set up and watch repositories with your coding agent", Args: cobra.ArbitraryArgs, RunE: func(c *cobra.Command, args []string) error {
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
			return fmt.Errorf("watching is paused; use watch start --resume to resume")
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
			statusf("Agent discovery enabled; automatic checkouts become inactive after 14 days without an agent edit.")
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
	addWatchSetupFlags(c)
	addWatchRepositoryCommands(c, app, api, profile)
	addWatchLifecycleCommands(c, app, api, profile)
	c.Flags().StringVar(&managedSession, "managed-session", "", "internal background startup identity")
	_ = c.Flags().MarkHidden("managed-session")
	c.Flags().BoolVar(&debug, "debug", false, "show repository discovery, scans, uploads, and review status (no source contents)")
	c.Flags().StringVar(&config, "config", "", "JSON file listing already-approved directories")
	c.Flags().StringVar(&base, "base", "", "local baseline ref")
	c.Flags().DurationVar(&interval, "interval", time.Minute, "fallback scan interval when filesystem events are missed")
	var state, hookAgent string
	var dispatch bool
	hook := &cobra.Command{Use: "hook", Hidden: true, RunE: func(c *cobra.Command, _ []string) error {
		if dispatch {
			return dispatchRuntimeHooks(c, *api, *profile, state, hookAgent)
		}
		expected, _, err := watchPaths(*api, *profile)
		if err != nil || state != expected {
			fmt.Fprint(c.OutOrStdout(), "{}")
			return nil
		}
		var cfg watching.Config
		if readWatchConfig(*api, *profile, &cfg) != nil {
			fmt.Fprint(c.OutOrStdout(), "{}")
			return nil
		}
		auth, e := watchAuth(app, *api, *profile)
		if e != nil || !watching.ConsentValid(cfg, *api, *profile, auth.Token) || paused(state) {
			fmt.Fprint(c.OutOrStdout(), "{}")
			return nil
		}
		cfg.RuntimeVersion = activeRuntimeVersion()
		raw, err := io.ReadAll(io.LimitReader(c.InOrStdin(), (1<<20)+1))
		if err != nil || len(raw) > 1<<20 {
			return fmt.Errorf("invalid hook input")
		}
		if cfg.RuntimeVersion != "" {
			var input watching.HookInput
			if json.Unmarshal(raw, &input) == nil {
				path, err := watching.HookRepository(c.Context(), input)
				if err == nil && watching.Allowed(cfg, path) && !watching.RepositoryRemoved(state, path) {
					if err := ensureRuntimeWatch(c.Context(), state, path, *api, *profile); err != nil {
						return err
					}
				}
			}
		}
		ctx, cancel := context.WithTimeout(c.Context(), 30*time.Second)
		defer cancel()
		watching.Hook(ctx, state, cfg, bytes.NewReader(raw), c.OutOrStdout(), hookAgent)
		return nil
	}}
	hook.Flags().BoolVar(&dispatch, "dispatch", false, "route events to isolated release runtimes")
	hook.Flags().StringVar(&state, "state", "", "private watch state directory")
	hook.Flags().StringVar(&hookAgent, "agent", "other", "coding agent identity")
	c.AddCommand(hook)
	var hypothesisState, hypothesisID string
	hypotheses := &cobra.Command{Use: "hypotheses", Hidden: true, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		expected, _, err := watchPaths(*api, *profile)
		if err != nil || hypothesisState != expected {
			return fmt.Errorf("invalid integration state")
		}
		var cfg watching.Config
		if err = readWatchConfig(*api, *profile, &cfg); err != nil {
			return err
		}
		if cfg.AccountKey == "" || cfg.API != *api || cfg.Profile != *profile || cfg.ConsentedAt.IsZero() {
			return fmt.Errorf("integration authorization unavailable")
		}
		ctx, cancel := context.WithTimeout(c.Context(), 10*time.Second)
		defer cancel()
		return watching.EmitHypotheses(ctx, expected, hypothesisID, cfg, c.InOrStdin(), c.OutOrStdout())
	}}
	hypotheses.Flags().StringVar(&hypothesisState, "state", "", "private integration state directory")
	hypotheses.Flags().StringVar(&hypothesisID, "request", "", "single-use edit request")
	c.AddCommand(hypotheses)
	var responseState, responseSession, responseRequest string
	respond := &cobra.Command{Use: "respond", Hidden: true, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		expected, _, err := watchPaths(*api, *profile)
		if err != nil || responseState != expected {
			return fmt.Errorf("invalid integration state")
		}
		var cfg watching.Config
		if err = readWatchConfig(*api, *profile, &cfg); err != nil {
			return err
		}
		if cfg.AccountKey == "" || cfg.API != *api || cfg.Profile != *profile || cfg.ConsentedAt.IsZero() {
			return fmt.Errorf("integration authorization unavailable")
		}
		return watching.EmitResponses(expected, responseSession, responseRequest, c.InOrStdin(), c.OutOrStdout())
	}}
	respond.Flags().StringVar(&responseState, "state", "", "private integration state directory")
	respond.Flags().StringVar(&responseSession, "session", "", "owning conversation")
	respond.Flags().StringVar(&responseRequest, "request", "", "turn-scoped response request")
	c.AddCommand(respond)

	var taskRepository, taskSession string
	task := &cobra.Command{Use: "task", Short: "Preserve the original task across a coding-agent restart (stdin)", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		root, _, err := watchPaths(*api, *profile)
		if err != nil {
			return err
		}
		var cfg watching.Config
		if err = readWatchConfig(*api, *profile, &cfg); err != nil {
			return err
		}
		repository, err := filepath.Abs(taskRepository)
		if err != nil {
			return err
		}
		repository, err = filepath.EvalSymlinks(repository)
		if err != nil {
			return err
		}
		if taskRepository == "" || cfg.API != *api || cfg.Profile != *profile || cfg.ConsentedAt.IsZero() || !watching.Allowed(cfg, repository) || watching.RepositoryRemoved(root, repository) {
			return fmt.Errorf("task repository is not approved for this profile")
		}
		if err = watching.SaveTaskHandoff(root, repository, taskSession, c.InOrStdin()); err != nil {
			return err
		}
		fmt.Fprintln(c.OutOrStdout(), "Original task saved locally for the next editing turn; nothing uploaded.")
		return nil
	}}
	task.Flags().StringVar(&taskRepository, "repo", "", "approved repository root")
	task.Flags().StringVar(&taskSession, "session", "", "owning coding-agent session")
	c.AddCommand(task)

	return c
}
