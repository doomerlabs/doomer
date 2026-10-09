package cmd

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/dependencies"
	"github.com/doomerlabs/doomer/internal/paths"
	"github.com/doomerlabs/doomer/internal/version"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/term"
)

func valueOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

type timer struct{ *time.Timer }

func (t timer) C() <-chan time.Time { return t.Timer.C }

type apiFactory struct{ store adversarylabs.ConfigStore }

func (f apiFactory) New(url string) application.APIClient {
	return adversarylabs.NewClientWithBaseURL(f.store, url)
}

type processTTY struct{}

func (processTTY) ReadSecret(ctx context.Context, r io.Reader, w io.Writer) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fmt.Fprint(w, "Password: ")
	f, ok := r.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		fmt.Fprintln(w)
		return nil, fmt.Errorf("interactive password input requires a terminal; use --password-stdin")
	}
	secret, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(w)
	return secret, err
}
func openBrowser(ctx context.Context, url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "open", url).Run()
	case "windows":
		return exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url).Run()
	default:
		return exec.CommandContext(ctx, "xdg-open", url).Run()
	}
}

func NewRootCommand() *cobra.Command {
	return newRootCommand(nil)
}

// An injected app allows auth tests to use isolated stores and local servers.
func newRootCommand(injected *application.App) *cobra.Command {
	var apiURL, profile string
	settings := viper.New()
	settings.SetEnvPrefix("DOOMER")
	settings.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	settings.AutomaticEnv()
	settings.SetDefault("profile", "default")
	root := &cobra.Command{Use: "doomer", Short: "Connect to the Doomer SaaS", SilenceUsage: true, SilenceErrors: true}
	app := &application.App{}
	root.PersistentFlags().String("profile", "", "credential profile (default: selected profile or default)")
	root.PersistentFlags().String("api-url", "", "SaaS API endpoint (default: https://doomer.ai/api)")
	_ = settings.BindPFlag("profile", root.PersistentFlags().Lookup("profile"))
	_ = settings.BindPFlag("api-url", root.PersistentFlags().Lookup("api-url"))
	root.PersistentPreRunE = func(command *cobra.Command, args []string) error {
		dir := os.Getenv("DOOMER_CONFIG_DIR")
		if dir == "" {
			var err error
			dir, err = paths.ConfigDir()
			if err != nil {
				return err
			}
		}
		settings.SetConfigFile(filepath.Join(dir, "settings.yaml"))
		if err := settings.ReadInConfig(); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("read settings: %w", err)
		}
		profile = strings.ToLower(strings.TrimSpace(settings.GetString("profile")))
		if !validProfile(profile) {
			return fmt.Errorf("profile must contain only letters, digits, underscores, or hyphens")
		}
		apiURL = settings.GetString("api-url")
		if apiURL == "" {
			apiURL = settings.GetString("profiles." + profile + ".api-url")
		}
		apiURL = adversarylabs.ResolveAPIURL(apiURL)
		if err := adversarylabs.ValidateAPIURL(apiURL); err != nil {
			return err
		}
		if injected != nil {
			app.Deps = injected.Deps
			return nil
		}
		store := adversarylabs.ConfigStore{Path: filepath.Join(dir, "config.json")}
		// Retain the old CLI's credential format and exact default-profile migration.
		if os.Getenv("DOOMER_CONFIG_DIR") == "" {
			var err error
			store, err = adversarylabs.DefaultConfigStore()
			if err != nil {
				return err
			}
		}
		app.Deps = application.Dependencies{
			Auth: store, API: apiFactory{store}, RegistryHost: adversarylabs.ResolveRegistryHost(),
			Clock: dependencies.Clock{NowFunc: time.Now, TimerFunc: func(d time.Duration) application.Timer { return timer{time.NewTimer(d)} }},
			TTY:   processTTY{}, BrowserAuth: dependencies.BrowserAuth{Entropy: rand.Reader, ListenFunc: net.Listen, NewServerFunc: dependencies.NewHTTPCallbackServer, OpenFunc: openBrowser},
		}
		switch command.Name() {
		case "pack", "push", "pull", "list", "inspect", "remove", "check":
			check, _ := command.Flags().GetBool("check")
			return configurePublishing(app, !(command.Name() == "pack" && check))
		}
		return nil
	}
	root.AddCommand(newLoginCommand(app, &apiURL, &profile, settings), newLogoutCommand(app, &apiURL, &profile), newProfileCommand(settings))
	root.AddCommand(newVersionCommand())
	root.AddCommand(newProjectCommand(app, &apiURL, &profile))
	root.AddCommand(newPackCommand(app), newPushCommand(app, &apiURL, &profile), newPullCommand(app, &apiURL, &profile), newArtifactsCommand(app))
	root.Version = fmt.Sprintf("%s (commit %s, built %s)", version.Version, version.Commit, version.BuildDate)
	root.SetVersionTemplate("doomer {{.Version}}\n")
	root.CompletionOptions.DisableDefaultCmd = true
	return root
}

func validProfile(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func writeSettings(settings *viper.Viper) error {
	file := settings.ConfigFileUsed()
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for _, path := range []string{dir, file} {
		info, err := os.Lstat(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || path == file && !info.Mode().IsRegular()) {
			return fmt.Errorf("refusing non-regular settings path")
		}
	}
	if err := settings.WriteConfigAs(file); err != nil {
		return err
	}
	return os.Chmod(file, 0600)
}

func newProfileCommand(settings *viper.Viper) *cobra.Command {
	profile := &cobra.Command{Use: "profile", Short: "Manage SaaS connection profiles"}
	profile.AddCommand(&cobra.Command{Use: "ls", Short: "List profiles without credentials", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		names := settings.GetStringMap("profiles")
		if names == nil {
			names = map[string]any{}
		}
		names["default"] = nil
		selected := settings.GetString("profile")
		names[selected] = nil
		keys := make([]string, 0, len(names))
		for name := range names {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			marker := " "
			if name == selected {
				marker = "*"
			}
			endpoint := settings.GetString("profiles." + name + ".api-url")
			if endpoint == "" {
				endpoint = adversarylabs.DefaultAPIURL
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s\t%s\n", marker, name, endpoint)
		}
		return nil
	}})
	var endpoint string
	add := &cobra.Command{Use: "add NAME", Short: "Save a profile's API endpoint", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.ToLower(strings.TrimSpace(args[0]))
		if !validProfile(name) {
			return fmt.Errorf("invalid profile name")
		}
		if err := adversarylabs.ValidateAPIURL(endpoint); err != nil {
			return err
		}
		// Use a separate Viper instance so command flags and environment overrides are never persisted.
		file := viper.New()
		file.SetConfigFile(settings.ConfigFileUsed())
		if err := file.ReadInConfig(); err != nil && !os.IsNotExist(err) {
			return err
		}
		file.Set("profiles."+name+".api-url", strings.TrimRight(endpoint, "/"))
		if err := writeSettings(file); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Saved profile %s.\n", name)
		return nil
	}}
	add.Flags().StringVar(&endpoint, "endpoint", adversarylabs.DefaultAPIURL, "SaaS API endpoint")
	profile.AddCommand(add)
	profile.AddCommand(&cobra.Command{Use: "use NAME", Short: "Select the default profile", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.ToLower(strings.TrimSpace(args[0]))
		if !validProfile(name) {
			return fmt.Errorf("invalid profile name")
		}
		file := viper.New()
		file.SetConfigFile(settings.ConfigFileUsed())
		if err := file.ReadInConfig(); err != nil && !os.IsNotExist(err) {
			return err
		}
		if name != "default" && !file.IsSet("profiles."+name) {
			return fmt.Errorf("unknown profile %q; use profile add first", name)
		}
		file.Set("profile", name)
		if err := writeSettings(file); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Selected profile %s.\n", name)
		return nil
	}})
	return profile
}
