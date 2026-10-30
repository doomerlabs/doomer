package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func prepareWatch(c *cobra.Command, app *application.App, api, profile *string, dirs []string) (watching.Config, adversarylabs.Auth, error) {
	root, file, err := watchPaths(*api, *profile)
	if err != nil {
		return watching.Config{}, adversarylabs.Auth{}, err
	}
	auth, err := watchAuth(app, *api, *profile)
	if err != nil {
		return watching.Config{}, auth, err
	}
	var cfg watching.Config
	err = watching.Read(file, &cfg)
	if err != nil && !os.IsNotExist(err) {
		return cfg, auth, fmt.Errorf("read watch configuration: %w", err)
	}
	valid := err == nil && cfg.Version == 1 && cfg.API == *api && cfg.Profile == *profile && !cfg.ConsentedAt.IsZero() && cfg.AccountKey == watching.Key(auth.Token)
	approved := valid
	for _, dir := range dirs {
		path, e := watching.Root(c.Context(), dir)
		if e != nil {
			return cfg, auth, e
		}
		approved = approved && watching.Allowed(cfg, path)
		if c.Name() == "add" && !cfg.AllRepositories {
			id, e := watching.RepositoryIdentity(c.Context(), path)
			if e != nil {
				return cfg, auth, e
			}
			repositoryApproved := false
			for _, existing := range cfg.RepositoryIDs {
				if existing == id {
					repositoryApproved = true
				}
			}
			approved = approved && repositoryApproved
		}
	}
	agents := "codex"
	if flag := c.Flags().Lookup("agent"); flag != nil {
		agents = flag.Value.String()
	}
	selected := []string{agents}
	if agents == "both" {
		selected = []string{"codex", "claude"}
	}
	if agents != "codex" && agents != "claude" && agents != "both" {
		return cfg, auth, fmt.Errorf("--agent must be codex, claude, or both")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return cfg, auth, err
	}
	installed := true
	marker := "watch hook --state " + watching.Quote(root)
	for _, agent := range selected {
		data, e := os.ReadFile(watching.HookFile(home, agent))
		installed = installed && e == nil && strings.Count(string(data), marker) >= 4
	}
	if approved && installed {
		return cfg, auth, nil
	}
	setup := newSetupCommand(app, api, profile)
	setup.SetContext(c.Context())
	setup.SetIn(c.InOrStdin())
	setup.SetOut(c.OutOrStdout())
	setup.SetErr(c.ErrOrStderr())
	var flags []string
	for _, name := range []string{"yes", "browser", "no-browser", "agent"} {
		if f := c.Flags().Lookup(name); f != nil && f.Changed {
			flags = append(flags, "--"+name+"="+f.Value.String())
		}
	}
	// Existing upload approval also permits repairing the selected integration.
	if approved {
		flags = []string{"--yes", "--agent=" + agents}
		dirs = cfg.Directories
		if cfg.AllRepositories {
			dirs = nil
		}
	}
	if err = setup.ParseFlags(flags); err != nil {
		return cfg, auth, err
	}
	if err = setup.RunE(setup, dirs); err != nil {
		return cfg, auth, err
	}
	if err = watching.Read(file, &cfg); err != nil {
		return cfg, auth, err
	}
	// Setup may have replaced credentials while obtaining approval.
	auth, err = watchAuth(app, *api, *profile)
	return cfg, auth, err
}

func addWatchSetupFlags(c *cobra.Command) {
	c.Flags().Bool("yes", false, "approve first-time uploads and hook installation without prompting")
	c.Flags().Bool("browser", false, "open first-time setup in a browser")
	c.Flags().Bool("no-browser", false, "complete first-time setup in the terminal")
	c.Flags().String("agent", "codex", "install codex, claude, or both integrations")
	c.MarkFlagsMutuallyExclusive("yes", "browser", "no-browser")
}

func addWatchRepositoryCommands(parent *cobra.Command, app *application.App, api, profile *string) {
	var worktrees bool
	list := &cobra.Command{Use: "ls", Short: "List watched repositories", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		root, file, err := watchPaths(*api, *profile)
		if err != nil {
			return err
		}
		var cfg watching.Config
		if err = watching.Read(file, &cfg); err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintln(c.OutOrStdout(), "No watched repositories. Run doomer watch to get started.")
				return nil
			}
			return err
		}
		repos, err := watching.Repositories(root, cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.OutOrStdout(), "Doomer watch · %s · %s\n", *profile, *api)
		return renderWatchRepositories(c, root, cfg, repos, worktrees)

	}}
	list.Flags().BoolVar(&worktrees, "worktrees", false, "show each repository’s checkouts as a tree")
	add := &cobra.Command{Use: "add <directories...>", Short: "Enable a repository and its linked worktrees", Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		_, _, err := prepareWatch(c, app, api, profile, args)
		if err != nil {
			return err
		}
		root, _, err := watchPaths(*api, *profile)
		if err != nil {
			return err
		}
		for _, dir := range args {
			path, err := watching.Root(c.Context(), dir)
			if err != nil {
				return err
			}
			if err = watching.SetRepositorySelection(root, path, false); err != nil {
				return err
			}
			if err = watching.RegisterRepository(root, path, "explicit", true, time.Time{}); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Added %s and enabled its linked worktrees.\n", path)
		}
		return nil
	}}
	addWatchSetupFlags(add)
	remove := &cobra.Command{Use: "rm <directories...>", Short: "Stop watching a repository and all its worktrees", Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		root, _, err := watchPaths(*api, *profile)
		if err != nil {
			return err
		}
		for _, dir := range args {
			path, err := watching.Root(c.Context(), dir)
			if err != nil {
				path, err = filepath.Abs(dir)
				if err != nil {
					return err
				}
				path = filepath.Clean(path)
			}
			if err = watching.SetRepositorySelection(root, path, true); err != nil {
				return err
			}
			if err = watching.RemoveRepository(root, path); err != nil {
				return err
			}
			fmt.Fprintf(c.OutOrStdout(), "Removed %s and its linked worktrees. Use watch add to enable the repository again.\n", path)
		}
		return nil
	}}
	uninstall := &cobra.Command{Use: "uninstall", Short: "Remove coding-agent hooks and revoke upload approval", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		setup := newSetupCommand(app, api, profile)
		setup.SetContext(c.Context())
		setup.SetIn(c.InOrStdin())
		setup.SetOut(c.OutOrStdout())
		setup.SetErr(c.ErrOrStderr())
		if err := setup.Flags().Set("uninstall", "true"); err != nil {
			return err
		}
		return setup.RunE(setup, nil)
	}}
	parent.AddCommand(list, add, remove, uninstall)
}
