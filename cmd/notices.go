package cmd

import (
	"fmt"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func newNoticesCommand(api *string) *cobra.Command {
	var deferID, ackID string
	var asJSON bool
	var revision int
	cmd := &cobra.Command{Use: "notices", Aliases: []string{"advisories"}, Short: "Check current Doomer advisories (no account needed)", Args: cobra.NoArgs, RunE: func(c *cobra.Command, args []string) error {
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
		if !asJSON {
			if len(result.Notices) == 0 {
				_, err := fmt.Fprintln(c.OutOrStdout(), "No current Doomer advisories.")
				return err
			}
			for _, n := range result.Notices {
				if _, err := fmt.Fprintln(c.OutOrStdout(), adversarylabs.AdvisoryTerminal(n)); err != nil {
					return err
				}
			}
			return nil
		}
		return writeJSON(c.OutOrStdout(), "notices", struct {
			adversarylabs.NoticeEnvelope
			Instructions string `json:"instructions"`
		}{result, adversarylabs.NoticeDeliveryInstructions})
	}}
	cmd.PersistentPostRun = func(*cobra.Command, []string) {}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print structured advisories and delivery instructions")
	cmd.Flags().StringVar(&deferID, "defer", "", "defer an optional advisory for seven days")
	cmd.Flags().StringVar(&ackID, "ack", "", "confirm an advisory was displayed to the user (local, no network)")
	cmd.Flags().IntVar(&revision, "revision", 0, "exact revision of the displayed advisory")
	_ = cmd.Flags().MarkHidden("ack")
	_ = cmd.Flags().MarkHidden("revision")
	cmd.MarkFlagsRequiredTogether("ack", "revision")
	cmd.MarkFlagsMutuallyExclusive("ack", "defer")
	return cmd
}
