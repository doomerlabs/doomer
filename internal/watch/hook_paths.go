package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var errHookOutsideRepository = errors.New("explicit working directory is outside Git")

var hookWorkdir = regexp.MustCompile(`(?:["']?(?:workdir|cwd)["']?)\s*:\s*("(?:[^"\\]|\\.)*"|'[^']*')`)
var hookPatchPath = regexp.MustCompile(`(?:^|\n|\\n)\*\*\* (?:Update File|Add File|Delete File|Move to): ([^\r\n\\"']+)`)

// HookRepository resolves the checkout touched by a hook.
// Tool paths take precedence over the chat's directory. Wrapped tool calls are
// inspected as text only; no command or JavaScript is evaluated here.
func HookRepository(ctx context.Context, input HookInput) (string, error) {
	return hookRepository(ctx, input, "")
}

func hookRepository(ctx context.Context, input HookInput, fallback string) (string, error) {
	candidates := []string{}
	explicitDirectory := false
	var inspect func(any)
	inspect = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for _, key := range []string{"workdir", "cwd", "path", "file_path", "filename"} {
				if p, ok := v[key].(string); ok && p != "" {
					candidates = append(candidates, p)
					if key == "workdir" || key == "cwd" {
						explicitDirectory = true
					}
				}
			}
			for _, key := range []string{"command", "cmd", "code", "patch", "input"} {
				if child, ok := v[key]; ok {
					inspect(child)
				}
			}
		case string:
			for _, match := range hookWorkdir.FindAllStringSubmatch(v, -1) {
				explicitDirectory = true
				raw := match[1]
				if len(raw) > 0 && raw[0] == '"' {
					if p, err := strconv.Unquote(raw); err == nil {
						candidates = append(candidates, p)
					}
				} else {
					candidates = append(candidates, strings.Trim(raw, "'"))
				}
			}
			for _, match := range hookPatchPath.FindAllStringSubmatch(v, -1) {
				candidates = append(candidates, strings.TrimSpace(match[1]))
			}
		}
	}
	var value any
	if json.Unmarshal(input.Input, &value) == nil {
		inspect(value)
	}
	roots := map[string]bool{}
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(input.CWD, candidate)
		}
		for {
			if info, err := os.Stat(candidate); err == nil {
				if !info.IsDir() {
					candidate = filepath.Dir(candidate)
				}
				if root, err := Root(ctx, candidate); err == nil {
					roots[root] = true
				}
				break
			}
			parent := filepath.Dir(candidate)
			if parent == candidate {
				break
			}
			candidate = parent
		}
	}
	if len(roots) > 1 {
		return "", fmt.Errorf("tool spans multiple repositories")
	}
	for root := range roots {
		return root, nil
	}
	if explicitDirectory {
		return "", errHookOutsideRepository
	}
	root, err := Root(ctx, input.CWD)
	// Only pathless events may reuse a repository selected earlier in this
	// turn. Explicit paths must never be redirected to another checkout.
	if err != nil && len(candidates) == 0 && fallback != "" {
		return Root(ctx, fallback)
	}
	return root, err
}
