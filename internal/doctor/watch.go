package doctor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func diagnostic(data object) object {
	result := fields(data, "At", "Event", "Tool", "ToolID", "HasToolID", "Repository", "Status")
	result["Owner"] = fields(nested(data, "Owner"), "session", "turn")
	conflict := nested(data, "Conflict")
	if conflict != nil {
		result["Conflict"] = fields(conflict, "Reason", "ToolID", "OtherToolID")
		result["Conflict"].(map[string]any)["OtherOwner"] = fields(nested(conflict, "OtherOwner"), "session", "turn")
	}
	return result
}

func (c collector) watchRoots() []any {
	configs := []string{filepath.Join(c.home, "Library/Application Support/doomer"), filepath.Join(c.home, ".config/doomer")}
	if root := os.Getenv("XDG_CONFIG_HOME"); root != "" {
		configs = append(configs, filepath.Join(root, "doomer"))
	}
	if root := os.Getenv("DOOMER_CONFIG_DIR"); root != "" {
		configs = append(configs, root)
	}
	sum := sha256.Sum256([]byte(c.repo))
	key := hex.EncodeToString(sum[:]) + ".json"
	result := []any{}
	seen := map[string]bool{}
	for _, configDir := range configs {
		base := filepath.Join(configDir, "watch")
		if info, err := os.Lstat(base); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		for _, shared := range directories(base, 30) {
			config := readObject(shared, "config.json")
			roots := []string{shared}
			if info, err := os.Lstat(filepath.Join(shared, "runtimes")); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				roots = append(roots, directories(filepath.Join(shared, "runtimes"), 30)...)
			}
			for _, root := range roots {
				if seen[root] {
					continue
				}
				seen[root] = true
				// Bound the complete report even when several legacy config roots coexist.
				if len(result) == 60 {
					return result
				}
				item := object{"path": root}
				_, err := os.Lstat(filepath.Join(shared, "paused.json"))
				item["paused"] = err == nil
				settings := fields(config, "version", "api_url", "profile", "consented_at", "explicit_consent", "all_repositories", "installation_id")
				dirs, _ := config["directories"].([]any)
				ids, _ := config["repository_ids"].([]any)
				settings["approved_directory_count"] = len(dirs)
				settings["approved_repository_id_count"] = len(ids)
				listed := false
				for _, dir := range dirs {
					if dir == c.repo {
						listed = true
					}
				}
				settings["exact_checkout_listed"] = listed
				item["config"] = settings
				process := fields(readObject(root, "process.json"), "pid", "session", "managed_session", "working_directory")
				process["pid_exists"] = processExists(process["pid"])
				item["process"] = process
				var heartbeat string
				_ = json.Unmarshal(readFile(root, "heartbeat.json"), &heartbeat)
				item["heartbeat"] = heartbeat
				state := readObject(root, filepath.Join("repositories", key))
				stateResult := fields(state, "Agent", "RequestedAt", "InlineWaited", "Ambiguous", "Fingerprint", "DiffBase")
				stateResult["Owner"] = fields(nested(state, "Owner"), "session", "turn")
				stateResult["Conflict"] = diagnostic(state)["Conflict"]
				active := []any{}
				for id, raw := range nested(state, "Active") {
					op, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					entry := fields(op, "Started", "Attributable", "CompletedAt", "Before")
					entry["tool_id"] = id
					entry["Owner"] = fields(nested(op, "Owner"), "session", "turn")
					active = append(active, entry)
					if len(active) == 50 {
						break
					}
				}
				stateResult["Active"] = active
				item["state"] = stateResult
				feed := readObject(root, filepath.Join("reviews", key))
				review := fields(feed, "ReviewID", "URL", "AssessmentStatus", "FailureCode", "Updated", "Anonymous", "Fingerprint", "BaseRef", "TargetTreeHash")
				review["Owner"] = fields(nested(feed, "Owner"), "session", "turn")
				item["review"] = review
				item["recent_hook_events"] = c.events(root)
				result = append(result, item)
			}
		}
	}
	return result
}
func (c collector) events(root string) []any {
	events := []any{}
	for _, folder := range []string{"hook-diagnostics", "hook-history", "codex-hook-receipts"} {
		directory := filepath.Join(root, folder)
		if info, err := os.Lstat(directory); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		files, _ := os.ReadDir(directory)
		type candidate struct {
			name     string
			modified int64
		}
		candidates := []candidate{}
		for _, file := range files {
			if file.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}
			if info, err := file.Info(); err == nil && info.Mode().IsRegular() {
				candidates = append(candidates, candidate{file.Name(), info.ModTime().UnixNano()})
			}
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].modified > candidates[j].modified })
		for _, file := range candidates[:min(len(candidates), 100)] {
			var data any
			if json.Unmarshal(readFile(root, filepath.Join(folder, file.name)), &data) != nil {
				continue
			}
			entries, ok := data.([]any)
			if !ok {
				entries = []any{data}
			}
			for _, raw := range entries[:min(len(entries), 200)] {
				entry, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				repo, _ := entry["Repository"].(string)
				if repo != "" {
					resolved, err := filepath.EvalSymlinks(repo)
					if err != nil || resolved != c.repo {
						continue
					}
				}
				events = append(events, diagnostic(entry))
			}
		}
	}
	sort.SliceStable(events, func(i, j int) bool {
		a, _ := events[i].(map[string]any)["At"].(string)
		b, _ := events[j].(map[string]any)["At"].(string)
		return a > b
	})
	return events[:min(len(events), 200)]
}
