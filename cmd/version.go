package cmd

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/doomerlabs/doomer/internal/version"
	"github.com/spf13/cobra"
)

func newVersionCommand() *cobra.Command {
	var format string
	command := &cobra.Command{Use: "version", Short: "Print the doomer version", Args: cobra.NoArgs,
		// Version and Homebrew checks work without reading profiles or credentials.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			switch format {
			case "text":
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "doomer %s (commit %s, built %s, %s)\n", version.Version, version.Commit, version.BuildDate, runtime.Version())
				return err
			case "json":
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"version": version.Version, "commit": version.Commit, "buildDate": version.BuildDate, "goVersion": runtime.Version()})
			default:
				return fmt.Errorf("unsupported format %q; use text or json", format)
			}
		},
	}
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return command
}
