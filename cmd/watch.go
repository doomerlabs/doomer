package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/paths"
	"github.com/doomerlabs/doomer/internal/publock"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func watchPaths(api, profile string) (string, string, error) {
	dir := os.Getenv("DOOMER_CONFIG_DIR")
	if dir == "" {
		var err error
		dir, err = paths.ConfigDir()
		if err != nil {
			return "", "", err
		}
	}
	root := filepath.Join(dir, "watch", watching.Key(api+"\x00"+profile))
	return root, filepath.Join(root, "config.json"), nil
}
func watchAuth(app *application.App, api, profile string) (adversarylabs.Auth, error) {
	auth, ok, err := scopedAuth(app.Dependencies().Auth, api, profile, app.Dependencies().RegistryHost)
	if err != nil {
		return auth, err
	}
	if !ok || strings.TrimSpace(auth.Token) == "" {
		return auth, fmt.Errorf("not logged in; run doomer --profile %s login", profile)
	}
	return auth, nil
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
		if e := watching.Read(file, &cfg); e != nil && !os.IsNotExist(e) {
			return fmt.Errorf("invalid watch configuration: %w", e)
		}
		if status {
			fmt.Fprintf(c.OutOrStdout(), "Profile: %s\nService: %s\n", *profile, *api)
			if cfg.AllRepositories {
				fmt.Fprintln(c.OutOrStdout(), "Upload approval: all repositories used with this profile.")
			}
			for _, p := range cfg.Directories {
				fmt.Fprintln(c.OutOrStdout(), p)
			}
			fmt.Fprintln(c.OutOrStdout(), "Codex hook trust is managed in /hooks. Run watch in a separate terminal.")
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
		marker := "watch hook --state " + watching.Quote(root)
		if uninstall {
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
		allRepositories := len(args) == 0
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
		consent := setupConsent{Profile: *profile, API: *api, AllRepositories: allRepositories, Directories: directories, Agents: selected}
		f, ok := c.InOrStdin().(*os.File)
		interactive := ok && term.IsTerminal(int(f.Fd()))
		approved := yes
		if !yes && !interactive {
			return fmt.Errorf("setup requires approval: run it in an interactive terminal, or pass --yes to allow source uploads and install hooks for this profile")
		}
		if !yes && !noBrowser && (browser || defaultSetupBrowser()) {
			approved, err = browserSetupConsent(c.Context(), consent, c.OutOrStdout(), openBrowser)
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
			consent.print(c.OutOrStdout())
			fmt.Fprint(c.OutOrStdout(), "\nInstall hooks and allow these uploads? Type yes to continue: ")
			if !terminalSetupConsent(c.Context(), f) {
				return fmt.Errorf("setup cancelled; no upload consent saved")
			}
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
		cfg = watching.Config{AllRepositories: allRepositories, Version: 1, API: *api, Profile: *profile, Directories: directories, ConsentedAt: time.Now(), AccountKey: watching.Key(auth.Token)}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		// Shell fallback is silent if the binary or profile is temporarily unavailable.
		prefix := ""
		if dir := os.Getenv("DOOMER_CONFIG_DIR"); dir != "" {
			absolute, e := filepath.Abs(dir)
			if e != nil {
				return e
			}
			prefix = "env DOOMER_CONFIG_DIR=" + watching.Quote(absolute) + " "
		}
		command := prefix + watching.Quote(exe) + " --profile " + watching.Quote(*profile) + " --api-url " + watching.Quote(*api) + " watch hook --state " + watching.Quote(root) + " 2>/dev/null || printf '{}'"
		if err = watching.Write(file, cfg); err != nil {
			return err
		}
		for _, agent := range selected {
			if err = watching.Install(watching.HookFile(home, agent), command, marker, false); err != nil {
				return err
			}
		}
		fmt.Fprintln(c.OutOrStdout(), "Setup saved. In Codex, open /hooks and trust the Doomer CLI hooks. Then run: doomer --profile "+*profile+" watch\nKeep the watcher running in its own terminal. Use setup --status or setup --uninstall to inspect or remove it.")
		return nil
	}}
	c.Flags().BoolVar(&yes, "yes", false, "approve uploads and hook installation for the selected profile without prompting")
	c.Flags().BoolVar(&browser, "browser", false, "open browser setup (interactive terminal required)")
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
	var interval time.Duration
	c := &cobra.Command{Use: "watch [directories...]", Short: "Watch approved repositories and return initial reviews to your coding agent", Args: cobra.ArbitraryArgs, RunE: func(c *cobra.Command, args []string) error {
		root, file, err := watchPaths(*api, *profile)
		if err != nil {
			return err
		}

		var cfg watching.Config
		if err = watching.Read(file, &cfg); err != nil {
			return fmt.Errorf("run doomer setup first: %w", err)
		}
		auth, err := watchAuth(app, *api, *profile)
		if err != nil {
			return err
		}
		if cfg.Version != 1 || cfg.API != *api || cfg.Profile != *profile || cfg.ConsentedAt.IsZero() || cfg.AccountKey != watching.Key(auth.Token) {
			return fmt.Errorf("upload consent does not match this login/profile; run doomer setup")
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
					return fmt.Errorf("directory not approved; run doomer setup %s", canonical)
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
		discover := cfg.AllRepositories && len(args) == 0
		if (!discover && len(dirs) == 0) || len(dirs) > 16 {
			return fmt.Errorf("configure between 1 and 16 repositories")
		}
		lock, err := publock.TryAcquire(root, "watcher")
		if err != nil {
			return fmt.Errorf("a watcher for this profile is already running")
		}
		defer lock.Close()
		watchStarted := time.Now()
		ctx, cancel := context.WithCancel(c.Context())
		defer cancel()
		heartbeat := filepath.Join(root, "heartbeat.json")
		_ = watching.Write(heartbeat, time.Now())
		defer os.Remove(heartbeat)
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
					if e != nil || watching.Key(active.Token) != cfg.AccountKey || watching.Read(file, &current) != nil || current.AccountKey != cfg.AccountKey || !current.ConsentedAt.Equal(cfg.ConsentedAt) {
						cancel()
						return
					}
				}
			}
		}()
		client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, *api)
		output := &watchOutput{w: c.OutOrStdout()}
		fmt.Fprintf(output, "Doomer watch · profile %s · %s\nPress Ctrl+C to stop.\n", *profile, *api)
		statusf := func(format string, args ...any) {
			fmt.Fprintf(output, "[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
		}
		debugf := func(format string, args ...any) {
			if debug {
				fmt.Fprintf(output, "[debug %s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
			}
		}
		debugf("scan interval=%s; discovery=%t; initial repositories=%d", interval, discover, len(dirs))
		var group sync.WaitGroup
		failures := make(chan error, 16)
		running := map[string]bool{}
		launch := func(path string) {
			if running[path] || len(running) >= 16 {
				return
			}
			running[path] = true
			statusf("Watching %s — waiting for agent edits", path)
			group.Add(1)
			go func() {
				defer group.Done()
				r := watching.Runner{Root: root, Config: cfg, Token: auth.Token, Client: client, Output: output, Interval: interval, Base: base, Debug: debug, StartedAt: watchStarted}
				if e := r.Run(ctx, path, false); e != nil {
					statusf("%s: watcher stopped: %s", path, e)
					failures <- e
				}
			}()
		}
		for _, dir := range dirs {
			launch(dir)
		}
		if discover {
			if len(running) == 0 {
				fmt.Fprintf(output, "No repositories discovered yet. Waiting for your coding-agent hooks.\nTo watch this checkout directly, restart with: doomer --profile %s --api-url %s watch .\n", watching.Quote(*profile), watching.Quote(*api))
			} else {
				statusf("Also watching for repositories discovered by your coding-agent hooks.")
			}
			lastDiscoveryLog := time.Time{}
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for ctx.Err() == nil {
				entries, discoveryErr := os.ReadDir(filepath.Join(root, "discovered"))
				if debug && time.Since(lastDiscoveryLog) >= 15*time.Second {
					debugf("discovery: %d registrations; %d repositories watching", len(entries), len(running))
					if discoveryErr != nil && !os.IsNotExist(discoveryErr) {
						debugf("discovery scan failed: %s", discoveryErr)
					}
					lastDiscoveryLog = time.Now()
				}
				for _, entry := range entries {
					var path string
					if watching.Read(filepath.Join(root, "discovered", entry.Name()), &path) == nil {
						canonical, e := watching.Root(ctx, path)
						if e == nil && canonical == path {
							launch(path)
						}
					}
				}
				select {
				case <-ctx.Done():
				case <-ticker.C:
				}
			}
		}
		group.Wait()
		close(failures)
		for e := range failures {
			return e
		}
		return nil
	}}
	c.Flags().BoolVar(&debug, "debug", false, "show repository discovery, scans, uploads, and review status (no source contents)")
	c.Flags().StringVar(&config, "config", "", "JSON file listing already-approved directories")
	c.Flags().StringVar(&base, "base", "", "local baseline ref")
	c.Flags().DurationVar(&interval, "interval", 2*time.Second, "local change scan interval")
	var state string
	hook := &cobra.Command{Use: "hook", Hidden: true, RunE: func(c *cobra.Command, _ []string) error {
		expected, _, err := watchPaths(*api, *profile)
		if err != nil || state != expected {
			fmt.Fprint(c.OutOrStdout(), "{}")
			return nil
		}
		var cfg watching.Config
		if watching.Read(filepath.Join(state, "config.json"), &cfg) != nil {
			fmt.Fprint(c.OutOrStdout(), "{}")
			return nil
		}
		auth, e := watchAuth(app, *api, *profile)
		if e != nil || cfg.AccountKey != watching.Key(auth.Token) || cfg.API != *api || cfg.Profile != *profile || cfg.ConsentedAt.IsZero() {
			fmt.Fprint(c.OutOrStdout(), "{}")
			return nil
		}
		ctx, cancel := context.WithTimeout(c.Context(), 30*time.Second)
		defer cancel()
		watching.Hook(ctx, state, cfg, c.InOrStdin(), c.OutOrStdout())
		return nil
	}}
	hook.Flags().StringVar(&state, "state", "", "private watch state directory")
	c.AddCommand(hook)
	var hypothesisState, hypothesisID string
	hypotheses := &cobra.Command{Use: "hypotheses", Hidden: true, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		expected, _, err := watchPaths(*api, *profile)
		if err != nil || hypothesisState != expected {
			return fmt.Errorf("invalid integration state")
		}
		var cfg watching.Config
		if err = watching.Read(filepath.Join(expected, "config.json"), &cfg); err != nil {
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
		if err = watching.Read(filepath.Join(expected, "config.json"), &cfg); err != nil {
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

	return c
}
