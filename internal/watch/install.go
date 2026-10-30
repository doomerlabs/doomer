package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Preserve unrelated hooks and settings. Reinstall replaces only this exact
// CLI profile's entries, and backs up the original settings before writing.
func Install(file, command, marker string, remove bool) error {
	object := map[string]any{}
	if data, err := os.ReadFile(file); err == nil {
		if json.Unmarshal(data, &object) != nil || object == nil {
			return fmt.Errorf("invalid hook settings: %s", file)
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if remove {
		return nil
	}
	hooks, ok := object["hooks"].(map[string]any)
	if !ok {
		if object["hooks"] != nil {
			return fmt.Errorf("invalid hooks in %s", file)
		}
		hooks = map[string]any{}
	}
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop"} {
		var groups []any
		if hooks[event] != nil {
			var valid bool
			groups, valid = hooks[event].([]any)
			if !valid {
				return fmt.Errorf("invalid %s hooks", event)
			}
		}
		kept := []any{}
		for _, raw := range groups {
			group, valid := raw.(map[string]any)
			if !valid {
				return fmt.Errorf("invalid hook group")
			}
			handlers, valid := group["hooks"].([]any)
			if !valid {
				return fmt.Errorf("invalid handlers")
			}
			remaining := []any{}
			for _, h := range handlers {
				v, ok := h.(map[string]any)
				if !ok {
					return fmt.Errorf("invalid handler")
				}
				c, _ := v["command"].(string)
				if !strings.Contains(c, marker) {
					remaining = append(remaining, h)
				}
			}
			if len(remaining) > 0 {
				group["hooks"] = remaining
				kept = append(kept, group)
			}
		}
		if !remove {
			group := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 32}}}
			if event == "PreToolUse" || event == "PostToolUse" {
				group["matcher"] = ".*"
			}
			kept = append(kept, group)
		}
		hooks[event] = kept
	}
	object["hooks"] = hooks
	if data, err := os.ReadFile(file); err == nil {
		if err = os.WriteFile(file+".before-doomer-"+time.Now().UTC().Format("20060102T150405.000000000"), data, 0600); err != nil {
			return err
		}
	}
	return Write(file, object)
}
func HookFile(home, agent string) string {
	if agent == "claude" {
		return filepath.Join(home, ".claude", "settings.json")
	}
	base := os.Getenv("CODEX_HOME")
	if base == "" {
		base = filepath.Join(home, ".codex")
	}
	return filepath.Join(base, "hooks.json")
}
