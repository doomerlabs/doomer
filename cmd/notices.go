package cmd

import (
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func newNoticesCommand(api *string) *cobra.Command {
	var deferID string
	cmd := &cobra.Command{Use: "notices", Short: "Check current Doomer update notices (no account needed)", RunE: func(c *cobra.Command, args []string) error {
		client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, *api)
		result, err := client.CheckNotices(c.Context())
		if err != nil {
			return err
		}
		if deferID != "" {
			if err = adversarylabs.DeferNotice(c.Context(), deferID); err != nil {
				return err
			}
		}
		if collected := adversarylabs.NoticesFromContext(c.Context()); collected != nil {
			result.Notices = collected
		}
		return writeJSON(c.OutOrStdout(), "notices", result)
	}}
	cmd.Flags().StringVar(&deferID, "defer", "", "defer an optional notice for seven days")
	return cmd
}
