package cmd

import (
	"fmt"
	"strings"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/oci"
	"github.com/spf13/cobra"
)

func newAdversaryCommand(app *application.App, apiURL, profile *string) *cobra.Command {
	root := &cobra.Command{Use: "adversary", Short: "Create, publish, and manage adversaries"}
	var format string
	list := &cobra.Command{Use: "ls", Short: "List local adversaries", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := validateFormat(format, false); err != nil {
			return err
		}
		entries, err := app.Dependencies().Repository.ReferenceEntries()
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
	var expectedDigest string
	remove := &cobra.Command{Use: "rm <reference>", Short: "Remove a local adversary reference", Long: "Remove a locally packed or pulled reference. Remote packages and shared local content are retained.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("if-digest") {
			if _, err := oci.ParseDigest(expectedDigest); err != nil {
				return fmt.Errorf("invalid --if-digest: %w", err)
			}
		}
		entries, err := app.Dependencies().Repository.ReferenceEntries()
		if err != nil {
			return err
		}
		value := strings.TrimSpace(args[0])
		var matches []int
		for i, entry := range entries {
			if value == entry.CanonicalReference {
				matches = []int{i}
				break
			}
			ref, err := app.Dependencies().References.Parse(entry.CanonicalReference)
			if err != nil {
				return err
			}
			if value == ref.ShortName() || value == ref.Repository || value == ref.ShortName()+":"+ref.Tag || value == ref.Repository+":"+ref.Tag || value == entry.Digest {
				matches = append(matches, i)
			}
		}
		if len(matches) == 0 {
			return fmt.Errorf("local adversary reference %q not found", value)
		}
		if len(matches) > 1 {
			return fmt.Errorf("local adversary reference %q is ambiguous; use a fully qualified reference from doomer adversary ls", value)
		}
		entry := entries[matches[0]]
		digest := entry.Digest
		if cmd.Flags().Changed("if-digest") {
			digest = expectedDigest
		}
		if err := app.Dependencies().Repository.DeleteRef(entry.CanonicalReference, digest); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed %s.\n", entry.CanonicalReference)
		return nil
	}}
	remove.Flags().StringVar(&expectedDigest, "if-digest", "", "remove only if the reference still points to this digest")
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
	root.AddCommand(newInitCommand(), newPackCommand(app), newPushCommand(app, apiURL, profile), newPullCommand(app, apiURL, profile), list, inspect, remove, check)
	return root
}
