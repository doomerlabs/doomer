package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/internal/livewatch"
	"github.com/spf13/cobra"
)

func newWatchAddCommand() *cobra.Command {
	c := &cobra.Command{Use: "add DIRECTORY", Short: "Include another repository in this request's running watcher", Args: cobra.ExactArgs(1)}
	attachWatchAddControl(c)
	return c
}

func attachWatchAddControl(c *cobra.Command) {
	var key string
	legacy := c.RunE
	c.PersistentPreRunE = func(c *cobra.Command, args []string) error {
		if c.Flags().Changed("control") || legacy == nil {
			return nil
		}
		for parent := c.Parent(); parent != nil; parent = parent.Parent() {
			if parent.PersistentPreRunE != nil {
				return parent.PersistentPreRunE(c, args)
			}
		}
		return nil
	}
	c.Flags().StringVar(&key, "control", "", "watch control key supplied by this request's hook")
	c.RunE = func(c *cobra.Command, args []string) error {
		if !c.Flags().Changed("control") && legacy != nil {
			return legacy(c, args)
		}
		if err := cobra.ExactArgs(1)(c, args); err != nil {
			return err
		}
		if !watchControlKey.MatchString(key) {
			return fmt.Errorf("a valid watch control key is required")
		}
		directory, err := filepath.Abs(args[0])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(c.Context(), 25*time.Second)
		defer cancel()
		if _, err := livewatch.AddDirectory(ctx, key, directory); err != nil {
			return fmt.Errorf("add directory to watcher: %w", err)
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"added": directory})
	}
}
