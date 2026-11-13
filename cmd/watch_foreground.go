package cmd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/doomerlabs/doomer/internal/livewatch"
	"github.com/doomerlabs/doomer/internal/paths"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Hook input carries review metadata and authorization, not a lifecycle policy.
// The hook owns the control key and decides when to stop this process.
type watchConfiguration struct {
	livewatch.Config
	ManagerHome string `json:"manager_home"`
}

func watchToken(c *cobra.Command, api string, state agentHookState, env watchEnvironment) (string, error) {
	if token := env.get("DOOMER_TOKEN"); token != "" {
		return token, nil
	}
	dir := env.get("DOOMER_CONFIG_DIR")
	if dir == "" {
		dir = state.CredentialDirs[api]
	}
	if dir == "" {
		var err error
		dir, err = paths.ConfigDir()
		if err != nil {
			return "", err
		}
	}
	settings := viper.New()
	settings.SetConfigFile(filepath.Join(dir, "settings.yaml"))
	settings.SetDefault("profile", "default")
	if profile := env.get("DOOMER_PROFILE"); profile != "" {
		settings.Set("profile", profile)
	}
	if err := settings.ReadInConfig(); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	profile := strings.ToLower(strings.TrimSpace(settings.GetString("profile")))
	if c.Flags().Changed("profile") || c.InheritedFlags().Changed("profile") {
		profile, _ = c.Flags().GetString("profile")
	}
	if !validProfile(profile) {
		return "", fmt.Errorf("invalid credential profile")
	}
	store := adversarylabs.ConfigStore{Path: filepath.Join(dir, "config.json")}
	auth, exists, err := store.StoredAuthE(adversarylabs.AuthKey(api, profile))
	if err != nil {
		return "", err
	}
	if exists {
		active, valid, err := store.ExactAuthE(adversarylabs.AuthKey(api, profile))
		if err != nil {
			return "", err
		}
		if !valid || auth.Token == "" {
			return "", fmt.Errorf("saved account credentials have expired; sign in again")
		}
		return active.Token, nil
	}
	if token := state.Identities[api]; token != "" {
		return token, nil
	}
	return "", fmt.Errorf("credentials are missing; run npx @doomerlabs/doomer onboard")
}

func defaultManagerHome() (string, error) {
	if home := os.Getenv("DOOMER_HOME"); home != "" {
		return filepath.Abs(home)
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "doomer-node"), nil
}

func newForegroundWatchCommand() *cobra.Command {
	var fromStdin bool
	c := &cobra.Command{Use: "watch [directory]", Short: "Watch recursively and review settled changes until stopped", Args: cobra.MaximumNArgs(1), Long: "Watch recursively and review settled changes until stopped.\n\nReads the nearest .doomer.env in the watched directory or its parents. Supported keys: DOOMER_API_URL, DOOMER_CONFIG_DIR, DOOMER_TOKEN, DOOMER_PROFILE.\nPrecedence: explicit flags, directory settings, inherited environment, saved defaults. Values are literal KEY=value assignments; no shell or direnv is executed. An empty DOOMER_TOKEN clears an inherited token."}
	c.PersistentPreRunE = func(command *cobra.Command, args []string) error {
		if command != c {
			for parent := c.Parent(); parent != nil; parent = parent.Parent() {
				if parent.PersistentPreRunE != nil {
					return parent.PersistentPreRunE(command, args)
				}
			}
		}
		return nil
	}
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	c.Flags().BoolVar(&fromStdin, "config-stdin", false, "read watch configuration from stdin")
	_ = c.Flags().MarkHidden("config-stdin")
	c.RunE = func(c *cobra.Command, args []string) error {
		var config watchConfiguration
		if fromStdin {
			if len(args) != 0 {
				return fmt.Errorf("use a directory or stdin configuration, not both")
			}
			if err := json.NewDecoder(io.LimitReader(c.InOrStdin(), 1<<20)).Decode(&config); err != nil {
				return err
			}
		} else {
			directory := "."
			if len(args) > 0 {
				directory = args[0]
			}
			absolute, err := filepath.Abs(directory)
			if err != nil {
				return err
			}
			config.Directory, err = filepath.EvalSymlinks(absolute)
			if err != nil {
				return err
			}
			config.ManagerHome, err = defaultManagerHome()
			if err != nil {
				return err
			}
			config.ID = rand.Text()
		}
		if !filepath.IsAbs(config.Directory) || !filepath.IsAbs(config.ManagerHome) || config.ID == "" {
			return fmt.Errorf("invalid watch configuration")
		}
		environment, err := directoryWatchEnvironment(config.Directory)
		if err != nil {
			return err
		}
		var state agentHookState
		if err := watching.Read(filepath.Join(config.ManagerHome, "state.json"), &state); err != nil {
			return fmt.Errorf("read upload approval: %w", err)
		}
		explicit := ""
		if c.Flags().Changed("api-url") || c.InheritedFlags().Changed("api-url") {
			explicit, _ = c.Flags().GetString("api-url")
		}
		config.API, err = agentHookAPI(explicit, environment.get("DOOMER_API_URL"), state.API)
		if err != nil {
			return err
		}
		if _, err := agentHookConsent(state, config.API, config.Agent, ""); err != nil {
			return err
		}
		config.Token, err = watchToken(c, config.API, state, environment)
		if err != nil {
			return err
		}

		allowed := func(ctx context.Context, path string) bool {
			var state agentHookState
			if _, err := os.Stat(filepath.Join(config.ManagerHome, "uninstalling.json")); err == nil {
				return false
			}
			if watching.Read(filepath.Join(config.ManagerHome, "state.json"), &state) != nil {
				return false
			}
			consent, err := agentHookConsent(state, config.API, config.Agent, path)
			return err == nil && (path == "" || watching.Allowed(consent, path))
		}
		if !allowed(c.Context(), "") {
			return fmt.Errorf("upload approval is required; run npx @doomerlabs/doomer onboard")
		}
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signals)
		stop := make(chan struct{})
		ctx, cancel := context.WithCancel(context.WithoutCancel(c.Context()))
		// A watcher owns its advisory cache; its responses carry notices to the
		// agent. Background stderr is not evidence of user-visible delivery.
		ctx = adversarylabs.WithNoticeCollector(ctx, config.ManagerHome, config.API, io.Discard)
		config.Notices = func() []adversarylabs.ClientNotice { return adversarylabs.NoticesFromContext(ctx) }
		config.AcknowledgeNotice = func(id string, revision int) error { return adversarylabs.AcknowledgeNotice(ctx, id, revision) }

		defer cancel()
		go func() {
			select {
			case <-signals:
				close(stop)
			case <-c.Context().Done():
				close(stop)
			case <-ctx.Done():
			}
		}()
		config.Stop = stop
		if !fromStdin {
			config.Report = func(result livewatch.Result) { _ = json.NewEncoder(c.OutOrStdout()).Encode(result) }
		}
		client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, config.API)
		noticeCtx, stopNotices := context.WithTimeout(ctx, 2*time.Second)
		noticesReady := make(chan struct{})
		config.NoticesReady = noticesReady
		go func() { defer close(noticesReady); defer stopNotices(); _, _ = client.CheckNotices(noticeCtx) }()

		return livewatch.Serve(ctx, config.Config, client, allowed)
	}
	return c
}
