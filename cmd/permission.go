package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/doomerlabs/doomer/internal/application"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

// Permission belongs to the saved repository grant, independently of runtime,
// watcher health, hook activation, and the coding agent's current session.
func newHasPermissionCommand(app *application.App, api, profile *string) *cobra.Command {
	var jsonOutput bool
	c := &cobra.Command{
		Use:   "has-permission [directory]",
		Short: "Read saved repository upload consent without changing setup",
		Long:  "Read saved upload consent for this repository and its approved worktrees. Prints true or false. A paused watcher retains consent. A nonzero exit status means stdout is invalid and must not be used as a permission decision. Output can be incomplete after a write failure. This command does not contact a server or write local state.",
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
	return c
}
