package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	var browserSession, browserOrigin string
	var allowUnconfiguredBrowserOrigin bool
	var requestApproval bool
	agent := "codex"
	if os.Getenv("CLAUDECODE") != "" {
		agent = "claude"
	}
	c := &cobra.Command{
		Use:   "has-permission [directory]",
		Short: "Read saved repository upload consent without changing setup",
		Long:  "Read saved upload consent for this repository and its approved worktrees. Prints true or false. Uses the Node installer's saved approval when present, otherwise checks legacy watcher consent. An explicit credential profile selects legacy consent unless --manager-home is supplied. Use --managed for managed review tasks; it verifies the current account with the service before reading the saved grant. Checks are read-only by default. With --request-approval and --browser-session, a human can approve uploads for the displayed checkout, service, and agent in an interactive terminal. A pause retains consent. Accept the decision only when the command exits successfully. Discard all output on error, including incomplete output after a write failure.",
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
	readPermission := c.RunE
	c.RunE = func(c *cobra.Command, args []string) error {
		if !c.Flags().Changed("browser-session") && !c.Flags().Changed("browser-origin") && !allowUnconfiguredBrowserOrigin {
			if requestApproval {
				return fmt.Errorf("--request-approval requires --browser-session")
			}
			return readPermission(c, args)
		}
		if strings.TrimSpace(browserSession) == "" || (allowUnconfiguredBrowserOrigin && !c.Flags().Changed("browser-origin")) {
			return fmt.Errorf("use --browser-session and specify --browser-origin for an unconfigured endpoint")
		}
		if jsonOutput {
			return fmt.Errorf("--browser-session cannot be combined with --json")
		}
		if requestApproval && (managedWorkflow || c.Flags().Changed("profile") || c.InheritedFlags().Changed("profile")) {
			return fmt.Errorf("--request-approval requires a personal Doomer installer")
		}
		path := "."
		if len(args) > 0 {
			path = args[0]
		}
		endpoint, err := doctorBrowserEndpoint(browserOrigin, path, allowUnconfiguredBrowserOrigin)
		if err != nil {
			return err
		}
		stdout := c.OutOrStdout()
		var output bytes.Buffer
		c.SetOut(&output)
		var checkErr error
		if requestApproval {
			checkErr = requestInstallerCheckoutApproval(c, managerHome, path, agent)
		}
		if checkErr == nil {
			checkErr = readPermission(c, args)
		}
		c.SetOut(stdout)

		status := "checkout_upload_check_failed"
		decision := strings.TrimSpace(output.String())
		if checkErr == nil {
			switch decision {
			case "true":
				status = "checkout_upload_approved"
			case "false":
				status = "checkout_upload_not_approved"
			default:
				checkErr = fmt.Errorf("couldn’t read the saved upload approval decision")
			}
		}
		if checkErr != nil {
			fmt.Fprintln(c.ErrOrStderr(), checkErr)
		} else {
			fmt.Fprintln(stdout, decision)
		}
		if err := sendDoctorBrowserResult(c.Context(), endpoint, browserSession, []string{status}); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Result sent to Doomer Doctor.")
		if checkErr != nil {
			return fmt.Errorf("couldn’t check checkout upload approval")
		}
		return nil
	}
	c.Flags().BoolVar(&jsonOutput, "json", false, "print the permission decision and repository identity as JSON")
	c.Flags().StringVar(&browserSession, "browser-session", "", "send the approval status to a waiting Doomer Doctor session")
	c.Flags().BoolVar(&requestApproval, "request-approval", false, "prompt in the terminal to allow uploads for this checkout and agent")
	c.Flags().StringVar(&browserOrigin, "browser-origin", "https://doomer.ai", "origin from the waiting troubleshooting page")
	c.Flags().BoolVar(&allowUnconfiguredBrowserOrigin, "allow-unconfigured-browser-origin", false, "send the approval status to an explicitly specified dev origin")
	c.Flags().BoolVar(&managedWorkflow, "managed", false, "verify the current account and check managed review consent")
	c.Flags().StringVar(&managerHome, "manager-home", "", "read approval from this Node installer state directory")
	c.Flags().StringVar(&agent, "agent", agent, "coding agent for installer or managed integration consent: codex or claude")
	return c
}

func printInstallerPermission(c *cobra.Command, home, profile, path, id, agent string, jsonOutput bool) (bool, error) {
	explicitHome := home != ""
	// Existing native profiles remain isolated from installer approvals. The
	// public command, which has neither override, discovers the installer's store.
	if !explicitHome && (profile != "default" || c.Flags().Changed("profile") || c.InheritedFlags().Changed("profile")) {
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
	environment, err := directoryWatchEnvironment(path)
	if err != nil {
		return true, err
	}
	api, err := agentHookAPI(explicitAPI, environment.get("DOOMER_API_URL"), state.API)
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
