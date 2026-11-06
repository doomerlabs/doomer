package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doomerlabs/doomer/internal/paths"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

func newSelfUpdateCommand() *cobra.Command {
	var required string
	var check, adopt, sideBySide, launcher bool
	c := &cobra.Command{Use: "self-update", Short: "Check or update the private CLI managed by doomer-review", Args: cobra.NoArgs, PersistentPreRunE: func(*cobra.Command, []string) error { return nil }, RunE: func(c *cobra.Command, _ []string) error {
		if required == "" {
			return fmt.Errorf("--version must be the stable version pinned by the skill")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		u := selfupdate.Updater{Home: home, Versioned: sideBySide}
		if launcher {
			if err := validateLauncherMigration(home); err != nil {
				return err
			}
			result, err := u.InstallLauncher(required, adopt)
			if err != nil {
				return err
			}
			if err := superviseInstalledProfiles(c.Context(), result); err != nil {
				return fmt.Errorf("launcher installed; watcher reconciliation failed: %w", err)
			}
			return json.NewEncoder(c.OutOrStdout()).Encode(result)
		}
		var result selfupdate.Status
		if check {
			result, err = u.Check(required)
		} else {
			result, err = u.Apply(c.Context(), required, adopt)
		}
		if err != nil {
			return err
		}
		if !check && sideBySide {
			launcher, err := advanceManagedLauncher(u, required)
			if err != nil {
				return fmt.Errorf("release installed; launcher update failed: %w", err)
			}
			if err := superviseInstalledProfiles(c.Context(), launcher); err != nil {
				return fmt.Errorf("release and launcher installed; watcher reconciliation failed: %w", err)
			}
			result.LauncherVersion = launcher.Version
		} else if !check {
			if err := superviseInstalledProfiles(c.Context(), result); err != nil {
				return fmt.Errorf("launcher installed; watcher reconciliation failed: %w", err)
			}
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(result)
	}}
	c.Flags().StringVar(&required, "version", "", "exact stable release pinned by the skill")
	c.Flags().BoolVar(&check, "check", false, "inspect ownership and version without downloads or writes")
	c.Flags().BoolVar(&adopt, "adopt", false, "replace an unmanaged file or symlink after explicit approval, keeping a backup")
	c.Flags().BoolVar(&sideBySide, "side-by-side", false, "install or verify an immutable release without replacing other versions")
	c.Flags().BoolVar(&launcher, "install-launcher", false, "install the verified release as the checkout-aware command launcher")
	c.MarkFlagsMutuallyExclusive("check", "adopt")
	c.MarkFlagsMutuallyExclusive("check", "install-launcher")
	return c
}

// Installing an older checkout pin must not regress the machine-wide launcher.
// A ready immutable release still completes any interrupted launcher upgrade.
func advanceManagedLauncher(u selfupdate.Updater, required string) (selfupdate.Status, error) {
	newest, err := u.NewestInstalled(required)
	if err != nil {
		return selfupdate.Status{}, err
	}
	required = newest
	global := u
	global.Versioned = false
	current, err := global.Check(required)
	if err != nil {
		return current, err
	}
	if current.State == "newer_installed" || current.State == "ready" {
		return current, nil
	}
	if !current.Managed && current.State != "missing" {
		return current, fmt.Errorf("global command is unmanaged; use self-update --install-launcher with explicit adoption")
	}
	if current.Managed {
		if err := runtimeversion.Supports(context.Background(), current.Path); err == nil {
			return u.InstallLauncher(required, false)
		}
	}
	if err := validateLauncherMigration(u.Home); err != nil {
		return current, err
	}
	return u.InstallLauncher(required, false)
}

// Supervision uses the current launcher, not the older pin being installed.
// The supervisor can stop idle protocol-1 workers from previous releases.
func superviseInstalledProfiles(ctx context.Context, launcher selfupdate.Status) error {
	dir := os.Getenv("DOOMER_CONFIG_DIR")
	if dir == "" {
		var err error
		dir, err = paths.ConfigDir()
		if err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "watch"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		shared := filepath.Join(dir, "watch", entry.Name())
		var cfg watching.Config
		if err := watching.Read(filepath.Join(shared, "config.json"), &cfg); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if err := ensureWatchSupervisorBinary(ctx, shared, cfg.API, cfg.Profile, launcher.Path, launcher.Version); err != nil {
			return err
		}
	}
	return nil
}
