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
	var check, adopt bool
	c := &cobra.Command{Use: "self-update", Short: "Check or update the private CLI managed by doomer-review", Args: cobra.NoArgs, PersistentPreRunE: func(*cobra.Command, []string) error { return nil }, RunE: func(c *cobra.Command, _ []string) error {
		if required == "" {
			return fmt.Errorf("--version must be the stable version pinned by the skill")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		u := selfupdate.Updater{Home: home}
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
	c.MarkFlagsMutuallyExclusive("check", "adopt")
	return c
}
