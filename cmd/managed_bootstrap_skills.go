package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doomerlabs/doomer/internal/managed"
)

// The skills CLI owns its global installs, including copied installs. A symlink
// alone does not establish ownership; use the installer's recorded skill entry.
func inspectManagedBootstrap(path, scope, release, base string) (managed.BootstrapUpdate, error) {
	update, err := managed.InspectBootstrap(path, scope, release)
	if err != nil || scope != "personal" {
		return update, err
	}
	lockPath := filepath.Join(base, ".agents", ".skill-lock.json")
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		lockPath = filepath.Join(stateHome, "skills", ".skill-lock.json")
	}
	data, err := os.ReadFile(lockPath)
	if os.IsNotExist(err) {
		return update, nil
	}
	if err != nil {
		return update, err
	}
	var lock struct {
		Version int `json:"version"`
		Skills  map[string]struct {
			Source     string `json:"source"`
			SourceType string `json:"sourceType"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		return update, fmt.Errorf("inspect skills installer lock %s: %w", lockPath, err)
	}
	if lock.Version < 1 || lock.Skills == nil {
		return update, fmt.Errorf("invalid skills installer lock at %s", lockPath)
	}
	entry, exists := lock.Skills["doomer-review"]
	if !exists {
		return update, nil
	}
	if entry.Source == "" || entry.SourceType == "" {
		return update, fmt.Errorf("invalid doomer-review entry in skills installer lock at %s", lockPath)
	}
	update.UpdateManager = "skills"
	update.ManualUpdateCommand = []string{"npx", "skills", "update", "doomer-review", "--global"}
	if update.State == "update_available" || update.State == "current" {
		update.Action = "Display this notice verbatim in your reply, including the complete command: \"Your personal Doomer skill is managed by npx skills. To check for and install its update, run `npx skills update doomer-review --global` yourself.\" This is an informational notice, not an approval request. Do not execute the command, run doomer bootstrap upgrade, edit the skill files or installer lock, or create a pull request. The bundled revision may be newer than the installer's source; the skills CLI checks that source for available updates. Continue the current task with its selected release."
	}
	return update, nil
}
