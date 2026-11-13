package cmd

import (
	"github.com/doomerlabs/doomer/internal/application"
	"github.com/spf13/cobra"
)

func newSupportCommand(app *application.App, api, profile *string) *cobra.Command {
	c := &cobra.Command{Use: "support", Short: "Collect diagnostics for troubleshooting"}
	bundle := newDoctorCommand(app, api, profile)
	bundle.Use = "bundle"
	bundle.Short = "Save a private diagnostic bundle locally; upload only with --upload"
	bundle.Long = "Save allowlisted installation and review diagnostics locally. Source, diffs, raw logs and credential files are excluded. Use --upload to explicitly send the bundle to Doomer support."
	var upload bool
	bundle.Flags().BoolVar(&upload, "upload", false, "send the diagnostic bundle to Doomer support")
	bundle.Flags().Lookup("no-upload").DefValue = "true"
	_ = bundle.Flags().Set("no-upload", "true")
	_ = bundle.Flags().MarkHidden("no-upload")
	bundle.PreRunE = func(command *cobra.Command, _ []string) error {
		if upload {
			return command.Flags().Set("no-upload", "false")
		}
		return command.Flags().Set("no-upload", "true")
	}
	c.AddCommand(bundle)
	return c
}
