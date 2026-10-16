package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type profileCredentials interface {
	ProfileExists(string, string) (bool, error)
	RemoveProfile(string, string) error
	RenameProfile(string, string, string) error
}

func newProfileManageCommand(app *application.App, settings *viper.Viper, rename bool) *cobra.Command {
	use, short, count := "rm NAME", "Remove a profile and its local credentials", 1
	if rename {
		use, short, count = "rename OLD NEW", "Rename a profile and preserve its logins", 2
	}
	return &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(count), RunE: func(cmd *cobra.Command, args []string) error {
		for i, name := range args {
			args[i] = strings.ToLower(strings.TrimSpace(name))
			if !validProfile(args[i]) {
				return fmt.Errorf("invalid profile name %q", name)
			}
		}
		oldName := args[0]
		if rename && oldName == args[1] {
			return fmt.Errorf("new profile name must differ from the current name")
		}
		file := viper.New()
		file.SetConfigFile(settings.ConfigFileUsed())
		if err := file.ReadInConfig(); err != nil && !os.IsNotExist(err) {
			return err
		}
		deps := app.Dependencies()
		store, ok := deps.Auth.(profileCredentials)
		if !ok {
			return fmt.Errorf("credential store does not support profile management")
		}
		exists, err := store.ProfileExists(oldName, deps.RegistryHost)
		if err != nil {
			return err
		}
		if oldName != "default" && !file.IsSet("profiles."+oldName) && !exists {
			return fmt.Errorf("unknown profile %q", oldName)
		}
		values := file.AllSettings()
		profiles := file.GetStringMap("profiles")
		if profiles == nil {
			profiles = make(map[string]any)
		}
		if rename {
			newName := args[1]
			if newName == "default" || file.IsSet("profiles."+newName) {
				return fmt.Errorf("profile %q already exists", newName)
			}
			if err := store.RenameProfile(oldName, newName, deps.RegistryHost); err != nil {
				return err
			}
			entry, ok := profiles[oldName]
			if !ok {
				endpoint := file.GetString("api-url")
				if endpoint == "" {
					endpoint = adversarylabs.DefaultAPIURL
				}
				entry = map[string]any{"api-url": endpoint}
			}
			profiles[newName] = entry
			if file.GetString("profile") == oldName || oldName == "default" && file.GetString("profile") == "" {
				values["profile"] = newName
			}
		} else {
			if err := store.RemoveProfile(oldName, deps.RegistryHost); err != nil {
				return err
			}
			if file.GetString("profile") == oldName {
				values["profile"] = "default"
			}
		}
		delete(profiles, oldName)
		values["profiles"] = profiles
		// Rebuild the file settings so deletion also removes Viper's stored key.
		updated := viper.New()
		updated.SetConfigFile(settings.ConfigFileUsed())
		if err := updated.MergeConfigMap(values); err != nil {
			return err
		}
		if err := writeSettings(updated); err != nil {
			return fmt.Errorf("profile credentials updated, but could not save settings: %w", err)
		}
		if rename {
			fmt.Fprintf(cmd.OutOrStdout(), "Renamed profile %s to %s.\n", oldName, args[1])
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Removed profile %s and its local credentials.\n", oldName)
		}
		return nil
	}}
}
