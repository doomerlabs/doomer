package cmd

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/version"
)

// Local builds opt in explicitly. Released binaries and production services
// always retain the signed-runtime selection path.
func developmentRuntime(api, profile string) bool {
	u, err := url.Parse(api)
	return os.Getenv("DOOMER_DEV_CLI") == "1" && version.Version == "dev" &&
		profile == "local-dev" && err == nil &&
		((u.Scheme == "https" && strings.HasSuffix(u.Hostname(), ".ngrok.app")) ||
			(u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback())) && u.Path == "/api" &&
		u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func managedTaskBinary(task managed.Task) (string, error) {
	if task.Release == "dev" {
		if !developmentRuntime(task.API, task.Profile) {
			return "", fmt.Errorf("local development tasks require the dev CLI, DOOMER_DEV_CLI=1, local-dev profile, and an HTTPS ngrok API")
		}
		return os.Executable()
	}
	home, err := taskRuntimeHome(task)
	if err != nil {
		return "", err
	}
	return runtimeversion.Binary(home, task.Release), nil
}
