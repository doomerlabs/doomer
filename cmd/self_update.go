package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/doomerlabs/doomer/internal/selfupdate"
	"github.com/spf13/cobra"
	"os"
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
