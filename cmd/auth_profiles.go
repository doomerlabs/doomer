package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/viper"
)

// Prepare a new profile without saving settings until authentication succeeds.
func loginProfile(deps application.Dependencies, apiURL, profile string, settings *viper.Viper, replace bool) (string, *viper.Viper, error) {
	auth, ok, err := scopedAuth(deps.Auth, apiURL, profile, deps.RegistryHost)
	if err != nil {
		return "", nil, err
	}
	if replace || !ok || strings.TrimSpace(auth.Token) == "" {
		return profile, nil, nil
	}
	file := viper.New()
	file.SetConfigFile(settings.ConfigFileUsed())
	if err := file.ReadInConfig(); err != nil && !os.IsNotExist(err) {
		return "", nil, err
	}
	for suffix := 2; ; suffix++ {
		name := fmt.Sprintf("%s-%d", profile, suffix)
		if file.IsSet("profiles." + name) {
			continue
		}
		// Reserve names with expired credentials too: they may be refreshed later.
		_, exists, err := deps.Auth.StoredAuthE(adversarylabs.AuthKey(apiURL, name))
		if err != nil {
			return "", nil, err
		}
		if exists {
			continue
		}
		file.Set("profiles."+name+".api-url", apiURL)
		file.Set("profile", name)
		return name, file, nil
	}
}
