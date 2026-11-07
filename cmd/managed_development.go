package cmd

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
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

// Keep installed development hooks independent of temporary go-run binaries.
func stageDevelopmentBinary(source, root string) (string, error) {
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	directory := filepath.Join(root, "development", digest)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	destination := filepath.Join(directory, filepath.Base(source))
	if existing, err := os.ReadFile(destination); err == nil && sha256.Sum256(existing) == sha256.Sum256(data) {
		return destination, nil
	}
	temporary, err := os.CreateTemp(directory, ".doomer-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Chmod(0700); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporary.Name(), destination); err != nil {
		return "", err
	}
	return destination, nil
}
