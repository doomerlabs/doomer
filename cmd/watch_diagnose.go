package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

func newWatchDiagnoseCommand() *cobra.Command {
	var home, agent string
	c := &cobra.Command{
		Use: "diagnose [directory]", Short: "Explain agent routing and upload approval without starting a review",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			directory := "."
			if len(args) > 0 {
				directory = args[0]
			}
			directory, err := filepath.Abs(directory)
			if err != nil {
				return err
			}
			directory, err = filepath.EvalSymlinks(directory)
			if err != nil {
				return err
			}
			if !slices.Contains(npmAgents, agent) {
				return fmt.Errorf("agent must be claude, codex or copilot")
			}
			if home == "" {
				home, err = defaultManagerHome()
				if err != nil {
					return err
				}
			}
			home, err = filepath.Abs(home)
			if err != nil {
				return err
			}
			var state agentHookState
			if err := watching.Read(filepath.Join(home, "state.json"), &state); err != nil {
				return fmt.Errorf("read manager state: %w", err)
			}
			environment, err := directoryWatchEnvironment(directory)
			if err != nil {
				return err
			}
			explicit := ""
			if c.Flags().Changed("api-url") || c.InheritedFlags().Changed("api-url") {
				explicit, _ = c.Flags().GetString("api-url")
			}
			api, err := agentHookAPI(explicit, environment.get("DOOMER_API_URL"), state.API)
			if err != nil {
				return err
			}
			// Report the winning override so a directory routing file cannot hide a service mismatch.
			source := "default"
			routingFile := nearestWatchEnvironmentFile(directory)
			if state.API != "" {
				source = "manager state"
			}
			if os.Getenv("DOOMER_API_URL") != "" {
				source = "process environment"
			}
			if _, ok := environment["DOOMER_API_URL"]; ok {
				source = routingFile
			}
			if explicit != "" {
				source = "--api-url"
			}
			credentialDir, profile, err := watchCredentialProfile(c, api, state, environment)
			if err != nil {
				return err
			}
			cfg, approvalError := agentHookConsent(state, api, agent, directory)
			approved := approvalError == nil && cfg.AllRepositories
			if approvalError == nil && !approved {
				for _, allowed := range cfg.Directories {
					if allowed == directory {
						approved = true
					}
				}
			}
			approvedServices := []string{}
			for _, grant := range state.Grants {
				if !grant.ApprovedAt.IsZero() {
					approvedServices = append(approvedServices, grant.API)
				}
			}
			next := "Routing and upload approval match. Check the agent's /hooks screen and make a small code change."
			if !approved {
				next = "Upload approval does not match this directory and service. Check the routing file before approving another service."
			}
			report := map[string]any{"directory": directory, "agent": agent, "executable": mustExecutable(), "manager_home": home, "routing_file": routingFile, "service": api, "service_source": source, "saved_service": state.API, "process_service": os.Getenv("DOOMER_API_URL"), "approved_services": approvedServices, "upload_approved": approved, "credential_directory": credentialDir, "profile": profile, "token_override_present": environment.get("DOOMER_TOKEN") != "", "next": next}
			encoder := json.NewEncoder(c.OutOrStdout())
			encoder.SetIndent("", "  ")
			return encoder.Encode(report)
		},
	}
	c.Flags().StringVar(&home, "manager-home", "", "Node manager state directory")
	c.Flags().StringVar(&agent, "agent", "claude", "Agent to diagnose: claude, codex or copilot")
	c.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	return c
}

func nearestWatchEnvironmentFile(directory string) string {
	for {
		candidate := filepath.Join(directory, ".doomer.env")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
		directory = parent
	}
}

func mustExecutable() string { path, _ := os.Executable(); return path }
