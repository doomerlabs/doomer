package cmd

import (
	"fmt"
	"strings"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func newFindingsCommand(app *application.App, apiURL, profile *string) *cobra.Command {
	command := &cobra.Command{Use: "findings", Short: "Read the evidence behind Doomer review findings"}
	command.AddCommand(&cobra.Command{
		Use:   "show <finding-id>",
		Short: "Print a review finding's full evidence as markdown",
		Long: `Read the full evidence and suggested fix for a finding using your existing
Doomer login. Copy the finding ID from the PR comment. The server checks access
to that finding's project; no project selector or secret-link token is required.
Review details must be enabled for the project.`,
		Example: `  doomer findings show <finding-id>
  doomer --profile work findings show <finding-id>`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deps := app.Dependencies()
			auth, ok, err := scopedAuth(deps.Auth, *apiURL, *profile, deps.RegistryHost)
			if err != nil {
				return err
			}
			if !ok || strings.TrimSpace(auth.Token) == "" {
				return fmt.Errorf("not logged in for profile %q; run doomer --profile %s login", *profile, *profile)
			}
			client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, *apiURL)
			markdown, err := client.FindingDetail(cmd.Context(), auth.Token, args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), markdown)
			return err
		},
	})
	return command
}
