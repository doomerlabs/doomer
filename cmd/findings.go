package cmd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

var findingIDPatternForCommand = regexp.MustCompile(`^[a-f0-9]{7,64}$`)

func newFindingCommand(app *application.App, apiURL, profile *string) *cobra.Command {
	command := &cobra.Command{Use: "finding", Short: "Read the evidence behind Doomer review findings"}
	var project string
	get := &cobra.Command{
		Use:   "get <finding-id>",
		Short: "Print a review finding's full evidence as markdown",
		Long: `Read the full evidence and suggested fix for a finding using your existing
Doomer login. Copy the finding ID from the PR comment. The server checks access
to that finding's project. Short IDs use --project or your selected default project.
Review details must be enabled for the project.`,
		Example: `  doomer finding get a1b2c3d --project my-project
  doomer --profile work finding get <finding-id>`,
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
			scope := strings.TrimSpace(project)
			if len(args[0]) < 64 {
				if !findingIDPatternForCommand.MatchString(args[0]) {
					return fmt.Errorf("invalid finding ID: use the ID from the Doomer review comment")
				}
				scope, err = resolveReviewProject(cmd.Context(), deps, *apiURL, *profile, scope, auth.Token)
				if err != nil {
					return err
				}
			}
			markdown, err := client.FindingDetail(cmd.Context(), auth.Token, args[0], scope)
			if err != nil {
				return err
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), markdown)
			return err
		},
	}
	get.Flags().StringVar(&project, "project", "", "Project slug or ID (defaults to the selected project)")
	command.AddCommand(get, newFindingListCommand(app, apiURL, profile))
	return command
}
