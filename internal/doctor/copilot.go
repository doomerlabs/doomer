package doctor

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

func (c collector) copilotHooks() []any {
	home := os.Getenv("COPILOT_HOME")
	if home == "" {
		home = filepath.Join(c.home, ".copilot")
	}
	name := filepath.Join("hooks", "doomer-node.json")
	user := copilotHook(home, name, "user")
	project := copilotHook(c.repo, filepath.Join(".github", name), "checkout")
	active := user
	if project["installation"] == "installed" || project["exists"] == true && user["installation"] == "missing" {
		active = project
	}
	if _, err := exec.LookPath("copilot"); err == nil || user["exists"] == true || project["exists"] == true {
		active["active_profile_agent"] = "copilot"
	}
	if user["installation"] == "installed" && project["installation"] == "installed" {
		active["duplicate_installation"] = true
		active["other_hook_path"] = user["path"]
	}
	return []any{user, project}
}

func copilotHook(root, relative, scope string) object {
	path := filepath.Join(root, relative)
	entry := object{"path": path, "scope": scope, "exists": false, "installation": "missing", "activation": "unverified"}
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return entry
	}
	entry["exists"] = true
	var config struct {
		Version int `json:"version"`
		Hooks   map[string][]struct {
			Type    string `json:"type"`
			Command string `json:"command"`
			Matcher string `json:"matcher"`
			Async   bool   `json:"async"`
		} `json:"hooks"`
	}
	if err != nil || json.Unmarshal(readFile(root, relative), &config) != nil || config.Version != 1 {
		entry["installation"] = "unreadable"
		return entry
	}
	var missing []string
	for _, event := range []string{"userPromptSubmitted", "preToolUse", "postToolUse", "agentStop", "sessionEnd"} {
		found := false
		for _, hook := range config.Hooks[event] {
			args := strings.Fields(hook.Command)
			if hook.Type == "command" && hook.Matcher == "" && !hook.Async && strings.Contains(hook.Command, " watch agent ") &&
				slices.Contains(args, "--agent=copilot") && slices.Contains(args, "--event="+event) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, event)
		}
	}
	entry["installation"] = "installed"
	if len(missing) != 0 {
		entry["installation"] = "partial"
		entry["missing_events"] = missing
	}
	return entry
}
