package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
	var managerHome string
	agent := "codex"
	if os.Getenv("CLAUDECODE") != "" {
		agent = "claude"
	}
	c := &cobra.Command{
		Use:   "has-permission [directory]",
		Short: "Read saved repository upload consent without changing setup",
		Long:  "Read saved upload consent for this repository and its approved worktrees. Prints true or false. Uses the Node installer's saved approval when present, otherwise checks legacy watcher consent. An explicit native config directory or credential profile selects legacy consent unless --manager-home is supplied. Use --managed for managed review tasks; it verifies the current account with the service before reading the saved grant. These checks do not write local state. A pause retains consent. Accept the decision only when the command exits successfully. Discard all output on error, including incomplete output after a write failure.",
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
				if agent != "codex" && agent != "claude" {
					return fmt.Errorf("agent must be codex or claude")
				}
				return printManagedPermission(c, app, *api, *profile, path, id, agent, jsonOutput)
			}
			if used, err := printInstallerPermission(c, managerHome, *profile, path, id, agent, jsonOutput); used || err != nil {
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
	c.Flags().BoolVar(&managedWorkflow, "managed", false, "verify the current account and check managed review consent")
	c.Flags().StringVar(&managerHome, "manager-home", "", "read approval from this Node installer state directory")
	c.Flags().StringVar(&agent, "agent", agent, "coding agent for installer or managed integration consent: codex or claude")
	return c
}

func printInstallerPermission(c *cobra.Command, home, profile, path, id, agent string, jsonOutput bool) (bool, error) {
	explicitHome := home != ""
	// Existing native profiles remain isolated from installer approvals. The
	// public command, which has neither override, discovers the installer's store.
	if !explicitHome && (os.Getenv("DOOMER_CONFIG_DIR") != "" || profile != "default" || c.Flags().Changed("profile") || c.InheritedFlags().Changed("profile")) {
		return false, nil
	}
	if home == "" {
		home = os.Getenv("DOOMER_HOME")
	}
	if home == "" {
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			userHome, err := os.UserHomeDir()
			if err != nil {
				return false, err
			}
			data = filepath.Join(userHome, ".local", "share")
		}
		home = filepath.Join(data, "doomer-node")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return false, err
	}
	var state agentHookState
	if err := watching.Read(filepath.Join(home, "state.json"), &state); err != nil {
		if os.IsNotExist(err) && !explicitHome {
			return false, nil
		}
		return true, fmt.Errorf("read installer approval: %w", err)
	}
	if state.Version != 1 {
		return true, fmt.Errorf("unsupported installer state version")
	}
	if agent != "codex" && agent != "claude" {
		return true, fmt.Errorf("agent must be codex or claude")
	}
	explicitAPI := ""
	if c.Flags().Changed("api-url") || c.InheritedFlags().Changed("api-url") {
		explicitAPI, _ = c.Flags().GetString("api-url")
	}
	api, err := agentHookAPI(explicitAPI, os.Getenv("DOOMER_API_URL"), state.API)
	if err != nil {
		return true, err
	}
	_, consentErr := agentHookConsent(state, api, agent, path)
	approved := consentErr == nil
	if _, err := os.Stat(filepath.Join(home, "uninstalling.json")); err == nil {
		approved = false
	} else if !os.IsNotExist(err) {
		return true, err
	}
	if jsonOutput {
		return true, json.NewEncoder(c.OutOrStdout()).Encode(struct {
			Approved     bool   `json:"approved"`
			Repository   string `json:"repository"`
			RepositoryID string `json:"repository_id"`
			API          string `json:"api_url"`
			Agent        string `json:"agent"`
			Source       string `json:"source"`
		}{approved, path, id, api, agent, "installer"})
	}
	_, err = fmt.Fprintln(c.OutOrStdout(), approved)
	return true, err
}

func printManagedPermission(c *cobra.Command, app *application.App, api, profile, path, id, agent string, jsonOutput bool) error {
	s, err := managedStoreLocation()
	if err != nil {
		return err
	}
	task := managed.Task{Repo: path, RepositoryID: id, API: api, Profile: profile, Agent: agent}
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
