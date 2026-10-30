package watch

import (
	"os"
	"path/filepath"
	"time"
)

// Latest event metadata only: never prompts, source, tool arguments or results.
type hookDiagnostic struct {
	At         time.Time
	Event      string
	Tool       string
	Repository string
	HasToolID  bool
	Status     string
}

func (r Runner) logHookDiagnostics(path string, after time.Time) time.Time {
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
