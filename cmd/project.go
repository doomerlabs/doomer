package cmd

import (
	"context"
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
			projects, err := accessibleProjects(cmd.Context(), client, auth.Token)
			if err != nil {
				return err
			}
			if len(projects) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No projects found.")
				return err
			}
			store, err := projectDefaults(deps.Auth)
			if err != nil {
				return err
			}
			selected, err := store.DefaultProject(adversarylabs.AuthKey(*apiURL, *profile))
			if err != nil {
				return err
			}
			if selected == "" && len(projects) == 1 {
				selected = projects[0].Slug
			}
			sort.Slice(projects, func(i, j int) bool { return projects[i].Slug < projects[j].Slug })
			output := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(output, "\tSLUG\tNAME"); err != nil {
				return err
			}
			for _, project := range projects {
				marker := ""
				if selected != "" && (project.Slug == selected || project.ID == selected) {
					marker = "*"
				}
				if _, err := fmt.Fprintf(output, "%s\t%s\t%s\n", marker, project.Slug, project.Name); err != nil {
					return err
				}
			}
			return output.Flush()
		},
	})
	project.AddCommand(&cobra.Command{
		Use: "use <slug>", Short: "Select the default project for this profile", Args: cobra.ExactArgs(1),
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
			projects, err := accessibleProjects(cmd.Context(), client, auth.Token)
			if err != nil {
				return err
			}
			requested := strings.TrimSpace(args[0])
			for _, candidate := range projects {
				if requested != "" && (candidate.Slug == requested || candidate.ID == requested) {
					store, err := projectDefaults(deps.Auth)
					if err != nil {
						return err
					}
					selection := candidate.ID
					if selection == "" {
						selection = candidate.Slug
					}
					if err := store.SetDefaultProject(adversarylabs.AuthKey(*apiURL, *profile), selection); err != nil {
						return err
					}
					_, err = fmt.Fprintf(cmd.OutOrStdout(), "Using project %s for profile %s.\n", candidate.Slug, *profile)
					return err
				}
			}
			return fmt.Errorf("project %q is not available to this profile; use doomer project ls", requested)
		},
	})
	return project
}

type defaultProjectStore interface {
	DefaultProject(string) (string, error)
	SetDefaultProject(string, string) error
}

func projectDefaults(store application.AuthStore) (defaultProjectStore, error) {
	defaults, ok := store.(defaultProjectStore)
	if !ok {
		return nil, fmt.Errorf("credential store does not support default projects")
	}
	return defaults, nil
}

func accessibleProjects(ctx context.Context, client adversarylabs.Client, token string) ([]adversarylabs.Team, error) {
	account, err := client.Whoami(ctx, token)
	if err != nil {
		return nil, err
	}
	projects := account.Teams
	if len(projects) == 0 && account.Team.Slug != "" {
		projects = []adversarylabs.Team{account.Team}
	}
	return projects, nil
}

func resolveReviewProject(ctx context.Context, deps application.Dependencies, apiURL, profile, explicit, token string) (string, error) {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit, nil
	}
	store, err := projectDefaults(deps.Auth)
	if err != nil {
		return "", err
	}
	selected, err := store.DefaultProject(adversarylabs.AuthKey(apiURL, profile))
	if err != nil {
		return "", err
	}
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, apiURL)
	projects, err := accessibleProjects(ctx, client, token)
	if err != nil {
		return "", err
	}
	if selected != "" {
		for _, project := range projects {
			if project.Slug == selected || project.ID == selected {
				return project.Slug, nil
			}
		}
		return "", fmt.Errorf("default project %q is no longer available; select one with doomer project use <slug> or pass --project", selected)
	}
	if len(projects) == 1 {
		return projects[0].Slug, nil
	}
	return "", fmt.Errorf("no default project selected; use doomer project ls and doomer project use <slug>, or pass --project")
}
