package cmd

import (
	"fmt"
	"github.com/doomerlabs/doomer/internal/application"
	"github.com/spf13/cobra"
)

func newArtifactsCommand(app *application.App) *cobra.Command {
	root := &cobra.Command{Use: "artifacts", Short: "Inspect and maintain locally packed or pulled adversaries"}
	var format string
	list := &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List local adversaries", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := validateFormat(format, false); err != nil {
			return err
		}
		entries, err := app.Dependencies().Resolver.Entries(10000)
		if err != nil {
			return err
		}
		if format == "json" {
			return writeJSON(cmd.OutOrStdout(), "artifacts-list", entries)
		}
		for _, e := range entries {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", e.Record.Name, e.Record.Version, e.CanonicalReference)
		}
		return nil
	}}
	list.Flags().StringVar(&format, "format", "text", "output format: text or json")
	inspect := &cobra.Command{Use: "inspect <reference>", Short: "Show local artifact metadata", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		record, err := app.Dependencies().Resolver.ResolveRecord(args[0])
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), "artifacts-inspect", record)
	}}
	remove := &cobra.Command{Use: "remove <reference> <expected-digest>", Short: "Remove a local reference only if its digest still matches", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return app.Dependencies().Repository.DeleteRef(args[0], args[1])
	}}
	check := &cobra.Command{Use: "check", Short: "Verify the local artifact repository", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		report, err := app.Dependencies().Repository.CheckAll()
		if err != nil {
			return err
		}
		if err := writeJSON(cmd.OutOrStdout(), "artifacts-check", report); err != nil {
			return err
		}
		if !report.Healthy {
			return fmt.Errorf("artifact repository check failed")
		}
		return nil
	}}
	root.AddCommand(list, inspect, remove, check)
	return root
}
