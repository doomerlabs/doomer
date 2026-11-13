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
			fmt.Fprintln(c.OutOrStdout(), "Free Look Closer enabled. No account needed. Source code and agent review responses are uploaded to "+*api+". Deep review requires signing in.")
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
		attemptID, err := watching.NewInstallationAttemptID()
		if err != nil {
			return err
		}
		cfg = watching.Config{InstallationAttemptID: attemptID, ActivationReporting: reportActivation, InstallationID: installationID, ExplicitConsent: true, AllRepositories: allRepositories, Version: 1, API: *api, Profile: *profile, Directories: directories, ConsentedAt: time.Now(), AccountKey: watching.Key(auth.Token)}
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
	c := newForegroundWatchCommand()
	addWatchSetupFlags(c)
	addWatchRepositoryCommands(c, app, api, profile)
	addWatchLifecycleCommands(c, app, api, profile)
	c.AddCommand(newWatchSupervisorCommand(api, profile), newManagedWatchCommand(app, api, profile))
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
				if err == nil && watching.Allowed(cfg, path) && !watching.RepositoryRemoved(state, path) && watching.HookStartsWork(input) {
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
	c.AddCommand(newAgentWatchHookCommand())
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
	c.AddCommand(newWatchRespondCommand(), newWatchInstructionsCommand(), newWatchReportCommand(), newWatchNoticeAckCommand())

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
