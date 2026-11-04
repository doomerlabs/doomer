// Package runtimeversion resolves checkout pins and immutable CLI locations.
package runtimeversion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var Release = regexp.MustCompile(`^20[0-9]{2}\.[1-9][0-9]?\.[1-9][0-9]?\.[1-9][0-9]*$`)
var heading = regexp.MustCompile(`(?m)^## Select CLI release (20[0-9]{2}\.[1-9][0-9]?\.[1-9][0-9]?\.[1-9][0-9]*)\r?$`)

func Binary(home, version string) string {
	return filepath.Join(home, ".local", "share", "doomer", "versions", version, "doomer")
}

// Current is empty for legacy or development installations.
func Current() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	version := filepath.Base(filepath.Dir(exe))
	if Release.MatchString(version) && filepath.Clean(exe) == Binary(home, version) {
		return version
	}
	return ""
}

// Pin uses the installed skill's existing release declaration as the source of truth.
func Pin(repo string) (string, string, error) {
	version, source := "", ""
	for _, agent := range []string{".agents", ".claude"} {
		path := filepath.Join(repo, agent, "skills", "doomer-review", "references", "install.md")
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if !info.Mode().IsRegular() || info.Size() > 64<<10 {
			return "", "", fmt.Errorf("invalid skill release reference %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", "", err
		}
		matches := heading.FindAllSubmatch(data, -1)
		if len(matches) != 1 {
			return "", "", fmt.Errorf("expected one CLI release pin in %s", path)
		}
		selected := string(matches[0][1])
		if version != "" && version != selected {
			return "", "", fmt.Errorf("conflicting Codex and Claude CLI pins in %s", repo)
		}
		version, source = selected, path
	}
	return version, source, nil
}

func CleanEnvironment(env []string) []string {
	result := make([]string, 0, len(env))
	for _, value := range env {
		if !strings.HasPrefix(value, "DOOMER_SELECTED_PIN=") {
			result = append(result, value)
		}
	}
	return result
}

// Supports prevents older releases from silently using legacy shared hook state.
func Supports(ctx context.Context, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, binary, "runtime-info").Output()
	var info struct {
		Protocol int `json:"protocol"`
	}
	if err != nil || json.Unmarshal(data, &info) != nil || info.Protocol != 1 {
		return fmt.Errorf("%s does not support isolated versioned runtimes; update that repository's skill before migrating its hooks", binary)
	}
	return nil
}
