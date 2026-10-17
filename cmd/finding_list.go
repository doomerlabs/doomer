package cmd

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func newFindingListCommand(app *application.App, apiURL, profile *string) *cobra.Command {
	var project string
	var limit, offset int
	command := &cobra.Command{
		Use: "ls", Short: "List stored review findings for a project", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 1 || limit > 200 || offset < 0 {
				return fmt.Errorf("limit must be between 1 and 200 and offset must be non-negative")
			}
			deps := app.Dependencies()
			auth, ok, err := scopedAuth(deps.Auth, *apiURL, *profile, deps.RegistryHost)
			if err != nil {
				return err
			}
			if !ok || strings.TrimSpace(auth.Token) == "" {
				return fmt.Errorf("not logged in for profile %q; run doomer --profile %s login", *profile, *profile)
			}
			scope, err := resolveReviewProject(cmd.Context(), deps, *apiURL, *profile, project, auth.Token)
			if err != nil {
				return err
			}
			client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, *apiURL)
			findings, err := client.ListFindings(cmd.Context(), auth.Token, scope, limit, offset)
			if err != nil {
				return err
			}
			if len(findings) == 0 {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "No findings found.")
				return err
			}
			output := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(output, "ID\tREPOSITORY\tPR\tADVERSARY"); err != nil {
				return err
			}
			clean := func(s string) string {
				return strings.Map(func(r rune) rune {
					if r < 32 || r == 127 {
						return ' '
					}
					return r
				}, s)
			}
			for _, finding := range findings {
				if _, err := fmt.Fprintf(output, "%s\t%s\t%d\t%s\n", clean(finding.ID), clean(finding.Repository), finding.PullRequest, clean(finding.Adversary)); err != nil {
					return err
				}
			}
			return output.Flush()
		},
	}
	command.Flags().StringVar(&project, "project", "", "Project slug or ID (defaults to the selected project)")
	command.Flags().IntVar(&limit, "limit", 50, "Maximum number of findings (1-200)")
	command.Flags().IntVar(&offset, "offset", 0, "Number of findings to skip")
	return command
}
