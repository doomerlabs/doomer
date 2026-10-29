package watch

import (
	"bytes"
	"context"
	"path/filepath"
	"sort"
	"strings"
)

// LinkedWorktrees asks Git for registered checkouts, never searches the disk.
// Consent is evaluated separately for every path; narrow directory approvals do
// not silently become approval for every checkout of that repository.
func LinkedWorktrees(ctx context.Context, cfg Config, roots []string) []string {
	found := map[string]bool{}
	seen := map[string]bool{}
	for _, root := range roots {
		common, err := filesystemGit(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
		if err != nil {
			continue
		}
		identity := strings.TrimSpace(string(common))
		if seen[identity] {
			continue
		}
		seen[identity] = true
		raw, err := filesystemGit(ctx, root, "worktree", "list", "--porcelain", "-z").Output()
		if err != nil || len(raw) > 4<<20 {
			continue
		}
		for _, record := range bytes.Split(raw, []byte{0, 0}) {
			candidate := ""
			skip := false
			for _, field := range bytes.Split(record, []byte{0}) {
				text := string(field)
				if strings.HasPrefix(text, "worktree ") {
					candidate = strings.TrimPrefix(text, "worktree ")
				}
				if text == "bare" || strings.HasPrefix(text, "prunable") {
					skip = true
				}
			}
			if skip || !filepath.IsAbs(candidate) {
				continue
			}
			canonical, err := Root(ctx, candidate)
			if err != nil || !Allowed(cfg, canonical) {
				continue
			}
			actual, err := filesystemGit(ctx, canonical, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
			if err == nil && strings.TrimSpace(string(actual)) == identity {
				found[canonical] = true
			}
		}
	}
	result := make([]string, 0, len(found))
	for path := range found {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}
