package cmd

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

func renderWatchRepositories(c *cobra.Command, root string, cfg watching.Config, repos []watching.Repository, expanded bool) error {
	groups := map[string][]watching.Repository{}
	for _, repo := range repos {
		id := repo.ID
		if id == "" {
			id = repo.Path
		}
		groups[id] = append(groups[id], repo)
	}
	ids := make([]string, 0, len(groups))
	name := func(id string) string {
		if filepath.Base(id) == ".git" {
			return filepath.Base(filepath.Dir(id))
		}
		return strings.TrimSuffix(filepath.Base(id), ".git")
	}
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if name(ids[i]) == name(ids[j]) {
			return ids[i] < ids[j]
		}
		return name(ids[i]) < name(ids[j])
	})
	names := map[string]int{}
	for _, id := range ids {
		names[name(id)]++
	}
	now := time.Now()
	status := func(repo watching.Repository) string {
		if !repo.Removed && !watching.Allowed(cfg, repo.Path) {
			return "Not approved"
		}
		return watching.RepositoryStatus(root, repo, now)
	}
	priority := map[string]int{"Removed": 0, "Not approved": 1, "Inactive": 2, "Not running": 3, "Starting": 4, "Watching": 5}
	w := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "REPOSITORY\tSTATUS")
	for _, id := range ids {
		group := groups[id]
		sort.Slice(group, func(i, j int) bool { return group[i].Path < group[j].Path })
		summary := "Removed"
		for _, repo := range group {
			if state := status(repo); priority[state] > priority[summary] {
				summary = state
			}
		}
		label := name(id)
		if names[label] > 1 {
			label += " (" + id + ")"
		}
		fmt.Fprintf(w, "%s\t%s\n", label, summary)
		if !expanded {
			continue
		}
		for i, repo := range group {
			prefix := "├── "
			if i == len(group)-1 {
				prefix = "└── "
			}
			state := status(repo)
			if state == "Inactive" && !repo.LastActivity.IsZero() {
				state += fmt.Sprintf(" · %d days", int(now.Sub(repo.LastActivity)/(24*time.Hour)))
			}
			fmt.Fprintf(w, "%s%s\t%s\t%s\n", prefix, watching.CheckoutLabel(c.Context(), repo.Path), state, repo.Path)
		}
	}
	return w.Flush()
}
