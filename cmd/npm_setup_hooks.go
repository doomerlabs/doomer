package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	watching "github.com/doomerlabs/doomer/internal/watch"
)

var npmHookEvents = []string{"UserPromptSubmit", "Stop", "Interrupt", "SessionEnd", "PermissionDenied", "PreToolUse", "PostToolUse", "PostToolUseFailure"}

type npmSettings struct {
	files     map[string][]string
	skillDirs []string
	agents    []string
	owner     string
}

func npmAgentSettings(o npmSetupOptions, selected []string) (npmSettings, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return npmSettings{}, err
	}
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	claude := os.Getenv("CLAUDE_CONFIG_DIR")
	if claude == "" {
		claude = filepath.Join(home, ".claude")
	}
	codex, err = filepath.Abs(codex)
	if err != nil {
		return npmSettings{}, err
	}
	claude, err = filepath.Abs(claude)
	if err != nil {
		return npmSettings{}, err
	}

	owner := "--owner=doomer-node"
	if os.Getenv("DOOMER_DEV_INSTALL") == "1" {
		owner += "-dev"
	}
	s := npmSettings{
		agents: selected, owner: owner,
		files: map[string][]string{
			"claude": {filepath.Join(claude, "settings.json")},
			"codex":  {filepath.Join(codex, "hooks.json")},
		},
		skillDirs: []string{
			filepath.Join(home, ".agents", "skills", "doomer"),
			filepath.Join(claude, "skills", "doomer"),
			filepath.Join(codex, "skills", "doomer"),
		},
	}
	if !slices.Contains(selected, "codex") {
		return s, nil
	}
	candidates := []string{filepath.Join(home, ".codex", "hooks.json")}
	root := filepath.Join(home, "Library", "Application Support", "Codex Accounts", "profiles")
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return s, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			candidates = append(candidates, filepath.Join(root, entry.Name(), "home", "hooks.json"))
		}
	}
	for _, file := range candidates {
		if slices.Contains(s.files["codex"], file) {
			continue
		}
		info, err := os.Lstat(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return s, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return s, err
		}
		if strings.Contains(string(data), s.owner) || strings.Contains(string(data), "--managed-task-adapter") {
			s.files["codex"] = append(s.files["codex"], file)
		}
	}
	return s, nil
}

func (s npmSettings) skillPresent() bool {
	has := func(i int) bool { return npmSkillExists(filepath.Join(s.skillDirs[i], "SKILL.md")) }
	if len(s.agents) == 0 {
		return has(0)
	}
	return (!slices.Contains(s.agents, "claude") || has(1)) &&
		(!slices.Contains(s.agents, "codex") || has(0) || has(2))
}

// Use Number to avoid rounding integer values in unrelated configuration.
type npmJSONObject = map[string]any

func npmReadSettings(path string) (npmJSONObject, []byte, error) {
	before, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return npmJSONObject{}, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var config npmJSONObject
	reader := json.NewDecoder(strings.NewReader(string(before)))
	reader.UseNumber()
	if !json.Valid(before) || reader.Decode(&config) != nil || config == nil {
		return nil, nil, fmt.Errorf("invalid settings at %s", path)
	}
	return config, before, nil
}

func npmObjects(value any) ([]npmJSONObject, bool) {
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	objects := make([]npmJSONObject, 0, len(list))
	for _, entry := range list {
		object, ok := entry.(npmJSONObject)
		if !ok {
			return nil, false
		}
		objects = append(objects, object)
	}
	return objects, true
}

func npmValidateHooks(config npmJSONObject) (npmJSONObject, error) {
	value, exists := config["hooks"]
	if !exists {
		return npmJSONObject{}, nil
	}
	hooks, ok := value.(npmJSONObject)
	if !ok {
		return nil, fmt.Errorf("invalid hook settings")
	}
	for _, event := range npmHookEvents {
		if groups, exists := hooks[event]; exists {
			objects, ok := npmObjects(groups)
			if !ok {
				return nil, fmt.Errorf("invalid %s hook groups", event)
			}
			for _, group := range objects {
				if _, ok := npmObjects(group["hooks"]); !ok {
					return nil, fmt.Errorf("invalid %s hook handlers", event)
				}
			}
		}
	}
	return hooks, nil
}

func (s npmSettings) validate() error {
	for _, agent := range s.agents {
		for _, file := range s.files[agent] {
			config, _, err := npmReadSettings(file)
			if err != nil {
				return err
			}
			if _, err := npmValidateHooks(config); err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
		}
	}
	return nil
}

func npmOwnsHook(h npmJSONObject, owner string) bool {
	command, _ := h["command"].(string)
	return slices.Contains(strings.Fields(command), owner)
}

func npmHookCommand(o npmSetupOptions, agent, owner string) (string, error) {
	path, home := o.executable, o.home
	if runtime.GOOS == "windows" {
		if strings.ContainsAny(path+home, "\"%\r\n") {
			return "", fmt.Errorf("unsupported characters in Windows Doomer paths")
		}
		return fmt.Sprintf("\"%s\" watch agent --agent=%s %s --manager-home=\"%s\"", path, agent, owner, home), nil
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	return fmt.Sprintf("%s watch agent --agent=%s %s --manager-home=%s", quote(path), agent, owner, quote(home)), nil
}

func npmEventTimeout(agent, event string) int {
	switch event {
	case "UserPromptSubmit":
		return 45
	case "Stop":
		return 60
	case "SessionEnd":
		if agent == "claude" {
			return 15
		}
	case "PermissionDenied":
		if agent == "claude" {
			return 5
		}
	case "Interrupt":
		if agent == "codex" {
			return 15
		}
	}
	return 0
}

func mergeNPMHooks(hooks npmJSONObject, agent, owner, command string) bool {
	changed := false
	other := "--owner=doomer-node-dev"
	dev := owner == other
	if dev {
		other = "--owner=doomer-node"
	}
	for _, event := range npmHookEvents {
		groups, _ := npmObjects(hooks[event])
		timeout := npmEventTimeout(agent, event)
		matcher := ""
		if event == "PermissionDenied" {
			matcher = "Bash"
		}
		ownedCount, matches := 0, false
		for _, group := range groups {
			handlers, _ := npmObjects(group["hooks"])
			for _, h := range handlers {
				if npmOwnsHook(h, owner) {
					ownedCount++
					configuredMatcher, _ := group["matcher"].(string)
					matches = h["type"] == "command" && h["command"] == command && h["async"] != true && (configuredMatcher == matcher || matcher == "" && configuredMatcher == ".*")
				}
			}
		}
		if timeout != 0 && ownedCount == 1 && matches || timeout == 0 && ownedCount == 0 {
			continue
		}
		changed = true
		kept := []npmJSONObject{}
		otherIndex := -1
		for _, group := range groups {
			handlers, _ := npmObjects(group["hooks"])
			filtered := []npmJSONObject{}
			for _, h := range handlers {
				if !npmOwnsHook(h, owner) {
					filtered = append(filtered, h)
					if otherIndex < 0 && npmOwnsHook(h, other) {
						otherIndex = len(kept)
					}
				}
			}
			if len(filtered) > 0 || len(handlers) == 0 {
				group["hooks"] = filtered
				kept = append(kept, group)
			}
		}
		if timeout > 0 {
			handler := npmJSONObject{"type": "command", "command": command, "timeout": timeout}
			if agent == "codex" {
				label := "Doomer"
				if dev {
					label += " development"
				}
				action := "stop watch"
				if event == "UserPromptSubmit" {
					action = "start watch"
				}
				handler["statusMessage"] = label + ": " + action
			}
			index := len(kept)
			if otherIndex >= 0 {
				index = otherIndex
				if dev {
					index++
				}
			}
			group := npmJSONObject{"hooks": []npmJSONObject{handler}}
			if matcher != "" {
				group["matcher"] = matcher
			}
			kept = slices.Insert(kept, index, group)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	return changed
}

func (s npmSettings) installHooks(o npmSetupOptions, state npmSetupState) error {
	var recorded []string
	if err := state.get("hookFiles", &recorded); err != nil {
		return err
	}
	for _, agent := range s.agents {
		recorded = appendUnique(recorded, s.files[agent]...)
	}
	if len(s.agents) != 0 {
		state.set("hookFiles", recorded)
		delete(state, "hookRoot")
		if err := writeNPMSetupState(o.home, state); err != nil {
			return err
		}
	}
	for _, agent := range s.agents {
		command, err := npmHookCommand(o, agent, s.owner)
		if err != nil {
			return err
		}
		for _, file := range s.files[agent] {
			if err := installNPMHookFile(file, agent, s.owner, command); err != nil {
				return err
			}
		}
	}
	return nil
}

func installNPMHookFile(file, agent, owner, command string) error {
	_, err := configureNPMHookFile(file, agent, owner, command, nil, nil)
	return err
}

func configureNPMHookFile(file, agent, owner, command string, inspect func(npmJSONObject, npmJSONObject) (string, bool, error), report doctorSettingsChange) (bool, error) {
	unlock, err := npmSetupLock(file)
	if err != nil {
		return false, err
	}
	defer unlock()
	read := npmReadSettings
	if inspect != nil {
		read = readDoctorAutoHookSettings
	}
	config, before, err := read(file)
	if err != nil {
		return false, err
	}
	hooks, err := npmValidateHooks(config)
	if err != nil {
		return false, err
	}
	if inspect != nil {
		checkedCommand, missing, err := inspect(config, hooks)
		if err != nil || !missing {
			return false, err
		}
		command = checkedCommand
	}
	if !mergeNPMHooks(hooks, agent, owner, command) {
		return false, nil
	}
	config["hooks"] = hooks
	if before != nil {
		backup := fmt.Sprintf("%s.before-doomer-node-%d", file, time.Now().UnixMilli())
		if err := writeNPMFile(backup, before, 0600); err != nil {
			return false, err
		}
	}
	if err := watching.Write(file, config); err != nil {
		return false, err
	}
	if report != nil {
		report(before, config)
	}
	return true, nil
}
