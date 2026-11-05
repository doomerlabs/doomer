package cmd

import (
	"context"
	"encoding/json"
	"fmt"
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
	var repo, output, name, email string
	var noUpload bool
	var tags []string
	var setupErr error
	command := &cobra.Command{
		Use: "doctor", Short: "Collect and upload a private support diagnostic report",
		Long: "Collect allowlisted installation, Git, skill, hook, watcher and review metadata and upload it to Doomer support. Includes repository/branch names and correlation IDs. Excludes source, diffs, raw logs and credential files. A private local copy is always saved. Use --no-upload to inspect it locally. Name, email and tags are optional self-reported support labels.",
		Args: cobra.NoArgs,
		// Collect diagnostics even when normal CLI configuration is broken.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			setupErr = cmd.Root().PersistentPreRunE(cmd, args)
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
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
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			report, err := doctor.Collect(ctx, repo, version.Version)
			if err != nil {
				return err
			}
			report["contact"] = map[string]string{"name": name, "email": email}
			report["tags"] = labels
			data, err := json.MarshalIndent(report, "", "  ")
			if err != nil {
				return err
			}
			data = append(data, '\n')
			if len(data) > doctor.MaxBytes {
				return fmt.Errorf("diagnostic report exceeds 4 MiB")
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
			fmt.Fprintf(cmd.OutOrStdout(), "Report saved to %s\n", output)
			if noUpload {
				return nil
			}
			if setupErr != nil {
				return fmt.Errorf("report saved locally; upload configuration is invalid: %w", setupErr)
			}
			auth, err := reviewAuth(app, *api, *profile)
			if err != nil {
				return fmt.Errorf("report saved locally; obtain upload credentials: %w", err)
			}
			client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, *api)
			result, err := client.UploadDoctorReport(ctx, auth.Token, json.RawMessage(data))
			if err != nil {
				return fmt.Errorf("report saved locally; upload failed: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Uploaded private support report %s\n", result.ID)
			return nil
		},
	}
	command.Flags().StringVar(&repo, "repo", ".", "affected Git checkout")
	command.Flags().StringVar(&output, "output", "", "new local report file (default: private temporary directory)")
	command.Flags().BoolVar(&noUpload, "no-upload", false, "save the report locally without uploading")
	command.Flags().StringVar(&name, "name", "", "self-reported name for support")
	command.Flags().StringVar(&email, "email", "", "self-reported contact email for support")
	command.Flags().StringArrayVar(&tags, "tag", nil, "support label as key=value (repeatable, maximum 10)")
	return command
}
