package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/managed"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

// Permission belongs to the saved repository grant, independently of runtime,
// watcher health, hook activation, and the coding agent's current session.
func newHasPermissionCommand(app *application.App, api, profile *string) *cobra.Command {
	var jsonOutput bool
	var managedWorkflow bool
	c := &cobra.Command{
		Use:   "has-permission [directory]",
		Short: "Read saved repository upload consent without changing setup",
		Long:  "Read saved upload consent for this repository and its approved worktrees. Prints true or false. By default, checks legacy watcher consent locally. Use --managed for managed review tasks; it verifies the current account with the service before reading the saved grant. Neither mode writes local state. A pause retains consent. Accept the decision only when the command exits successfully. Discard all output on error, including incomplete output after a write failure.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			path := "."
			if len(args) > 0 {
				path = args[0]
			}
			path, err := watching.Root(c.Context(), path)
			if err != nil {
				return err
			}
			id, err := watching.RepositoryIdentity(c.Context(), path)
			if err != nil {
				return err
			}
			if managedWorkflow {
				return printManagedPermission(c, app, *api, *profile, path, id, jsonOutput)
			}
			root, file, err := consentPaths(*api, *profile)
			if err != nil {
				return err
			}
			var cfg watching.Config
			if err := watching.Read(file, &cfg); err != nil && !os.IsNotExist(err) {
				return err
			}
			auth, err := watchStatusAuth(app, *api, *profile, root)
			if err != nil {
				return err
			}
			approved := watching.ConsentValid(cfg, *api, *profile, auth.Token) && watching.Allowed(cfg, path)
			if jsonOutput {
				return json.NewEncoder(c.OutOrStdout()).Encode(struct {
					Approved     bool   `json:"approved"`
					Repository   string `json:"repository"`
					RepositoryID string `json:"repository_id"`
				}{approved, path, id})
			}
			_, err = fmt.Fprintln(c.OutOrStdout(), approved)
			return err
		},
	}
	c.Flags().BoolVar(&jsonOutput, "json", false, "print the permission decision and repository identity as JSON")
	c.Flags().BoolVar(&managedWorkflow, "managed", false, "verify the current account and check managed review consent")
	return c
}

func printManagedPermission(c *cobra.Command, app *application.App, api, profile, path, id string, jsonOutput bool) error {
	s, err := managedStoreLocation()
	if err != nil {
		return err
	}
	task := managed.Task{Repo: path, RepositoryID: id, API: api, Profile: profile}
	_, account, err := managedIdentity(c.Context(), app, task)
	if err != nil {
		return err
	}
	task.AccountID = account
	evidence, err := savedManagedConsentEvidence(s, task)
	if err != nil {
		return err
	}
	if jsonOutput {
		return json.NewEncoder(c.OutOrStdout()).Encode(struct {
			Approved     bool                    `json:"approved"`
			Repository   string                  `json:"repository"`
			RepositoryID string                  `json:"repository_id"`
			Consent      *managedConsentEvidence `json:"consent,omitempty"`
		}{evidence != nil, path, id, evidence})
	}
	_, err = fmt.Fprintln(c.OutOrStdout(), evidence != nil)
	return err
}
