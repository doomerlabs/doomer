package watch

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Local event metadata only: never prompts, source, tool arguments or results.
type hookDiagnostic struct {
	At         time.Time
	Event      string
	Tool       string
	ToolID     string
	Conflict   *AttributionConflict `json:",omitempty"`
	Repository string
	HasToolID  bool
	Status     string
	Owner      Owner
}

// Keep the latest event for existing readers, plus a bounded per-chat history
// so a later successful hook does not erase the cause of an earlier failure.
func recordHookDiagnostic(root, session string, diagnostic hookDiagnostic) {
	_ = Write(filepath.Join(root, "hook-diagnostics", Key(session+diagnostic.Event)+".json"), diagnostic)
	_ = Locked(root, "hook-history", func() error {
		file := filepath.Join(root, "hook-history", Key(session)+".json")
		var history []hookDiagnostic
		_ = Read(file, &history)
		history = append(history, diagnostic)
		if len(history) > 128 {
			history = history[len(history)-128:]
		}
		return Write(file, history)
	})
}

// Retain a week for debugging, removing inactive histories under the same
// lock used by writers and rechecking their age after acquiring it.
func pruneHookHistory(root string, before time.Time) {
	directory := filepath.Join(root, "hook-history")
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(before) {
			continue
		}
		_ = Locked(root, "hook-history", func() error {
			file := filepath.Join(directory, entry.Name())
			info, err := os.Stat(file)
			if err != nil || !info.ModTime().Before(before) {
				return err
			}
			return os.Remove(file)
		})
	}
}

func (r Runner) logHookDiagnostics(path string, after time.Time) time.Time {
	pruneHookHistory(r.Root, time.Now().Add(-7*24*time.Hour))
	entries, _ := os.ReadDir(filepath.Join(r.Root, "hook-diagnostics"))
	latest := after
	for _, entry := range entries {
		var d hookDiagnostic
		if Read(filepath.Join(r.Root, "hook-diagnostics", entry.Name()), &d) != nil || !d.At.After(after) || (d.Repository != "" && d.Repository != path) {
			continue
		}
		r.debugf("hook event=%s tool=%s tool_id=%t repository=%s status=%s", d.Event, d.Tool, d.HasToolID, d.Repository, d.Status)
		if d.At.After(latest) {
			latest = d.At
		}
		if time.Since(d.At) > time.Hour {
			_ = os.Remove(filepath.Join(r.Root, "hook-diagnostics", entry.Name()))
		}
	}
	return latest
}
