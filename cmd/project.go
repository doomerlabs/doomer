package cmd

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func newProjectCommand(app *application.App, apiURL, profile *string) *cobra.Command {
	project := &cobra.Command{Use: "project", Short: "Manage projects"}
	project.AddCommand(&cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List your projects with their slug and name",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps := app.Dependencies()
			auth, ok, err := scopedAuth(deps.Auth, *apiURL, *profile, deps.RegistryHost)
			if err != nil {
				return err
			}
			if !ok || strings.TrimSpace(auth.Token) == "" {
				return fmt.Errorf("not logged in for profile %q; run doomer --profile %s login", *profile, *profile)
			}
			client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, *apiURL)
			account, err := client.Whoami(cmd.Context(), auth.Token)
			if err != nil {
				return err
			}
			projects := account.Teams
			// Scoped service-account and CI tokens return only their own team.
			if len(projects) == 0 && account.Team.Slug != "" {
				projects = []adversarylabs.Team{account.Team}
			}
			if len(projects) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No projects found.")
				return err
			}
			sort.Slice(projects, func(i, j int) bool { return projects[i].Slug < projects[j].Slug })
			output := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(output, "SLUG\tNAME"); err != nil {
				return err
			}
			for _, project := range projects {
				if _, err := fmt.Fprintf(output, "%s\t%s\n", project.Slug, project.Name); err != nil {
					return err
				}
			}
			return output.Flush()
		},
	})
	return project
}
