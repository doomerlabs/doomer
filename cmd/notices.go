package cmd

import (
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func newNoticesCommand(api *string) *cobra.Command {
	var deferID, ackID string
	var revision int
	cmd := &cobra.Command{Use: "notices", Short: "Check current Doomer update notices (no account needed)", Args: cobra.NoArgs, RunE: func(c *cobra.Command, args []string) error {
		if c.Flags().Changed("ack") {
			if err := adversarylabs.AcknowledgeNotice(c.Context(), ackID, revision); err != nil {
				return err
			}
			return writeJSON(c.OutOrStdout(), "notice_acknowledgement", map[string]any{"id": ackID, "revision": revision, "displayed": true})
		}
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
		return writeJSON(c.OutOrStdout(), "notices", struct {
			adversarylabs.NoticeEnvelope
			Instructions string `json:"instructions"`
		}{result, adversarylabs.NoticeDeliveryInstructions})
	}}
	cmd.Flags().StringVar(&deferID, "defer", "", "defer an optional notice for seven days")
	cmd.Flags().StringVar(&ackID, "ack", "", "confirm a notice was displayed to the user (local, no network)")
	cmd.Flags().IntVar(&revision, "revision", 0, "exact revision of the displayed notice")
	cmd.MarkFlagsRequiredTogether("ack", "revision")
	cmd.MarkFlagsMutuallyExclusive("ack", "defer")
	return cmd
}
