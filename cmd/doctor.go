package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/doctor"
	"github.com/doomerlabs/doomer/internal/version"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func newDoctorCommand(app *application.App, api, profile *string) *cobra.Command {
	var repo, output, name, email, description, reportPath string
	var browserSession, browserOrigin string
	var allowUnconfiguredBrowserOrigin bool
	var noUpload, upload, asJSON, testSession bool
	var tags []string
	var setupErr error
	command := &cobra.Command{
		Use: "doctor", Short: "Diagnose Doomer locally and prepare a private support report",
		Long: "Check installation, hooks and recent review activity. A private report is saved locally. To send the exact report you inspected, run doomer doctor --upload --report PATH. Reports include repository and branch names and correlation IDs. They exclude source, diffs, raw logs and credential files.",
		Args: cobra.NoArgs,
		// Collect diagnostics even when normal CLI configuration is broken.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if upload && !noUpload && !testSession && !cmd.Flags().Changed("browser-session") && !cmd.Flags().Changed("browser-origin") && !allowUnconfiguredBrowserOrigin {
				setupErr = cmd.Root().PersistentPreRunE(cmd, args)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if allowUnconfiguredBrowserOrigin && (!cmd.Flags().Changed("browser-session") || !cmd.Flags().Changed("browser-origin")) {
				return fmt.Errorf("--allow-unconfigured-browser-origin requires --browser-session and --browser-origin")
			}
			if cmd.Flags().Changed("browser-session") || cmd.Flags().Changed("browser-origin") {
				if cmd.Flags().Changed("api-url") || cmd.InheritedFlags().Changed("api-url") {
					return fmt.Errorf("use --browser-origin with --browser-session instead of --api-url")
				}
				for _, flag := range []string{"upload", "no-upload", "report", "output", "json", "email", "description", "name", "tag", "test-session"} {
					if cmd.Flags().Changed(flag) {
						return fmt.Errorf("--browser-session cannot be combined with --%s", flag)
					}
				}
				endpoint, err := doctorBrowserEndpoint(browserOrigin, repo, allowUnconfiguredBrowserOrigin)
				if err != nil {
					return err
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
				defer cancel()
				report, collectionErr := doctor.Collect(ctx, repo, version.Version)
				checks := []string{}
				if collectionErr != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), collectionErr)
					checks = append(checks, "collection_failed")
				} else {
					for _, value := range report["checks"].([]any) {
						check := value.(map[string]any)
						checks = append(checks, check["code"].(string))
						fmt.Fprintln(cmd.OutOrStdout(), check["message"])
					}
				}
				if err := sendDoctorBrowserResult(cmd.Context(), endpoint, browserSession, checks); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Result sent to Doomer Doctor.")
				return nil
			}
			if testSession {
				for _, flag := range []string{"upload", "report", "output", "json", "email", "description", "name", "tag", "repo"} {
					if cmd.Flags().Changed(flag) {
						return fmt.Errorf("--test-session cannot be combined with --%s", flag)
					}
				}
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "Paste this entire prompt in the affected Codex or Claude Code chat:\n\n"+doctor.SessionPrompt)
				return err
			}
			if upload && noUpload {
				return fmt.Errorf("--upload and --no-upload cannot be combined")
			}
			if reportPath != "" && (!upload || asJSON || output != "" || name != "" || email != "" || description != "" || len(tags) != 0 || cmd.Flags().Changed("repo")) {
				return fmt.Errorf("--report requires --upload; prepare and preview report contents before sending")
			}
			if asJSON && upload {
				return fmt.Errorf("--json is only available for local diagnostics")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			if reportPath != "" {
				data, err := readDoctorReport(reportPath)
				if err != nil {
					return err
				}
				return uploadDoctorReport(ctx, cmd, app, *api, *profile, setupErr, data)
			}
			labels := map[string]string{}
			if len(tags) > 10 {
				return fmt.Errorf("at most 10 tags are allowed")
			}
			for _, tag := range tags {
				key, value, ok := strings.Cut(tag, "=")
				if !ok || len(key) == 0 || len(key) > 64 || len(value) > 256 {
					return fmt.Errorf("tags must use key=value with keys up to 64 bytes and values up to 256 bytes")
				}
				labels[key] = value
			}
			if len(name) > 200 || len(email) > 320 {
				return fmt.Errorf("name or email is too long")
			}
			if len(description) > 2000 {
				return fmt.Errorf("description exceeds 2000 bytes")
			}
			report, err := doctor.Collect(ctx, repo, version.Version)
			if err != nil {
				return err
			}
			report["contact"] = map[string]string{"name": name, "email": email}
			report["tags"] = labels
			if description != "" {
				report["description"] = doctor.RedactDescription(description)
			}
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			data = append(data, '\n')
			if len(data) > doctor.MaxBytes {
				return fmt.Errorf("diagnostic report exceeds 4 MiB")
			}
			if asJSON && output == "" {
				_, err := cmd.OutOrStdout().Write(data)
				return err
			}
			if output == "" {
				directory, err := os.MkdirTemp("", "doomer-support-")
				if err != nil {
					return err
				}
				output = filepath.Join(directory, "doomer-diagnostics.json")
			}
			// Never overwrite an existing report or follow a destination symlink.
			file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			_, writeErr := file.Write(data)
			closeErr := file.Close()
			if writeErr != nil {
				_ = os.Remove(output)
				return writeErr
			}
			if closeErr != nil {
				_ = os.Remove(output)
				return closeErr
			}
			if asJSON {
				_, err := cmd.OutOrStdout().Write(data)
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Report saved to %s\n", output)
			for _, check := range report["checks"].([]any) {
				evidence := check.(map[string]any)
				fmt.Fprintln(cmd.OutOrStdout(), evidence["message"])
				if next := evidence["next"]; next != nil {
					fmt.Fprintln(cmd.OutOrStdout(), next)
				}
			}
			executable, _ := filepath.EvalSymlinks(mustExecutable())
			configs, _ := report["hook_configs"].([]any)
			for _, entry := range configs {
				item, _ := entry.(map[string]any)
				agent, _ := item["active_profile_agent"].(string)
				if agent == "" {
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s hooks: %v; activation: %v (%s)\n", agent, item["installation"], item["activation"], item["path"])
				if item["installation"] != "installed" {
					if isMDMRuntimePath(executable) {
						fmt.Fprintf(cmd.OutOrStdout(), "Check the %s hook configuration with your administrator.\n", agent)
					} else {
						fmt.Fprintf(cmd.OutOrStdout(), "Install hooks: doomer hooks install --agent %s\n", agent)
					}
				}
			}
			if !upload {
				return nil
			}
			return uploadDoctorReport(ctx, cmd, app, *api, *profile, setupErr, data)
		},
	}
	command.Flags().BoolVar(&testSession, "test-session", false, "print the prompt for a live hook and watcher test in the affected chat")
	command.Flags().StringVar(&browserSession, "browser-session", "", "send status codes to a waiting troubleshooting page")
	command.Flags().StringVar(&browserOrigin, "browser-origin", "https://doomer.ai", "origin from the waiting troubleshooting page")
	command.Flags().BoolVar(&allowUnconfiguredBrowserOrigin, "allow-unconfigured-browser-origin", false, "send browser diagnostic status codes to an explicitly specified dev origin")
	command.Flags().StringVar(&repo, "repo", ".", "affected Git checkout")
	command.Flags().StringVar(&output, "output", "", "new local report file (default: private temporary directory)")
	command.Flags().BoolVar(&noUpload, "no-upload", false, "save the report locally without uploading (default)")
	command.Flags().BoolVar(&asJSON, "json", false, "print local diagnostics as JSON")
	command.Flags().BoolVar(&upload, "upload", false, "explicitly send a private diagnostic report to Doomer")
	command.Flags().StringVar(&reportPath, "report", "", "send an existing, previewed report without collecting again")
	command.Flags().StringVar(&description, "description", "", "brief description of what went wrong (optional)")
	command.Flags().StringVar(&name, "name", "", "self-reported name for support")
	command.Flags().StringVar(&email, "email", "", "self-reported contact email for support")
	command.Flags().StringArrayVar(&tags, "tag", nil, "support label as key=value (repeatable, maximum 10)")
	return command
}

func readDoctorReport(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > doctor.MaxBytes {
		return nil, fmt.Errorf("report must be a regular JSON file no larger than 4 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("report changed before it could be opened")
	}
	data, err := io.ReadAll(io.LimitReader(file, doctor.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	var header struct {
		Schema      int    `json:"schema"`
		CollectedAt string `json:"collected_at_utc"`
	}
	if len(data) > doctor.MaxBytes || json.Unmarshal(data, &header) != nil || header.Schema != 1 || header.CollectedAt == "" {
		return nil, fmt.Errorf("invalid Doomer diagnostic report")
	}
	return data, nil
}

func uploadDoctorReport(ctx context.Context, cmd *cobra.Command, app *application.App, api, profile string, setupErr error, data []byte) error {
	if setupErr != nil {
		return fmt.Errorf("report retained locally; upload configuration is invalid: %w", setupErr)
	}
	auth, err := reviewAuth(app, api, profile)
	if err != nil {
		return fmt.Errorf("report retained locally; obtain upload credentials: %w", err)
	}
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, api)
	result, err := client.UploadDoctorReport(ctx, auth.Token, json.RawMessage(data))
	if err != nil {
		return fmt.Errorf("report retained locally; upload failed: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Uploaded private support report %s\n", result.ID)
	return nil
}
