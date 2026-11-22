package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

type doctorRepairChange struct {
	Path string `json:"path"`
	Diff string `json:"diff"`
}

type doctorRepairChanges []doctorRepairChange

type doctorSettingsChange func(before []byte, after npmJSONObject)

func (changes *doctorRepairChanges) settings(path string, project func(npmJSONObject) npmJSONObject) doctorSettingsChange {
	return func(before []byte, after npmJSONObject) {
		old := npmJSONObject{}
		_ = json.Unmarshal(before, &old)
		written, err := json.Marshal(after)
		if err != nil {
			return
		}
		var current npmJSONObject
		if err := json.Unmarshal(written, &current); err != nil {
			return
		}
		oldJSON, _ := json.MarshalIndent(project(old), "", "  ")
		newJSON, _ := json.MarshalIndent(project(current), "", "  ")
		diff, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A: difflib.SplitLines(string(oldJSON) + "\n"), B: difflib.SplitLines(string(newJSON) + "\n"),
			FromFile: "before", ToFile: "after",
		})
		if diff != "" {
			*changes = append(*changes, doctorRepairChange{Path: path, Diff: diff})
		}
	}
}

func doctorHookDiffSettings(config npmJSONObject, command string) npmJSONObject {
	hooks, _ := config["hooks"].(npmJSONObject)
	owned := npmJSONObject{}
	for _, event := range npmHookEvents {
		groups, _ := npmObjects(hooks[event])
		for _, group := range groups {
			handlers, _ := npmObjects(group["hooks"])
			for _, handler := range handlers {
				if handler["command"] == command && handler["type"] == "command" {
					entry := npmJSONObject{"type": "command", "command": command}
					switch timeout := handler["timeout"].(type) {
					case int, float64, json.Number:
						entry["timeout"] = timeout
					}
					if status, _ := handler["statusMessage"].(string); slices.Contains([]string{
						"Doomer: start watch", "Doomer: stop watch",
						"Doomer development: start watch", "Doomer development: stop watch",
					}, status) {
						entry["statusMessage"] = status
					}
					row := npmJSONObject{"hooks": []npmJSONObject{entry}}
					if matcher, _ := group["matcher"].(string); matcher == "Bash" || matcher == ".*" {
						row["matcher"] = matcher
					}
					owned[event] = []npmJSONObject{row}
				}
			}
		}
	}
	return npmJSONObject{"hooks": owned}
}

func doctorPermissionDiffSettings(config npmJSONObject, rules, auto []string) npmJSONObject {
	scoped := npmJSONObject{}
	for _, kind := range []struct {
		key   string
		rules []string
	}{{"permissions", rules}, {"autoMode", auto}} {
		block, _ := config[kind.key].(npmJSONObject)
		values, _ := block["allow"].([]any)
		allowed := []string{}
		for _, value := range values {
			if rule, ok := value.(string); ok && (slices.Contains(kind.rules, rule) || kind.key == "autoMode" && rule == "$defaults") {
				allowed = append(allowed, rule)
			}
		}
		scoped[kind.key] = npmJSONObject{"allow": allowed}
	}
	return scoped
}

func (changes *doctorRepairChanges) skill(target doctorAutoTarget) {
	path := filepath.Join(filepath.Dir(target.file), "skills", "doomer")
	if link, err := os.Readlink(path); err == nil {
		*changes = append(*changes, doctorRepairChange{Path: path, Diff: fmt.Sprintf("--- /dev/null\n+++ symlink\n@@ -0,0 +1 @@\n+%s\n", link)})
	}
}

func (changes *doctorRepairChanges) codexPolicy(path, policy string) {
	lines := strings.Split(policy, "\n")
	// Scope data includes an account identifier and a credential path.
	lines[2] = "[Doomer account and checkout scope]"
	diff, _ := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: nil, B: difflib.SplitLines(strings.Join(lines, "\n")),
		FromFile: "before", ToFile: "after Codex auto_review.extra_policy",
	})
	if diff != "" {
		*changes = append(*changes, doctorRepairChange{Path: path, Diff: diff})
	}
}
