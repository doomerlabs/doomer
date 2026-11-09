// Package doctor collects allowlisted support metadata without source files or raw logs.
package doctor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/doomerlabs/doomer/internal/watch"
)

const MaxBytes = 4 * 1024 * 1024

type object = map[string]any

type collector struct{ repo, home string }

// Collect reads metadata for the affected checkout and preserves IDs needed for support.
func Collect(ctx context.Context, directory, version string) (map[string]any, error) {
	root, err := command(ctx, directory, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("run doctor from a Git checkout or pass --repo: %w", err)
	}
	root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	c := collector{repo: root, home: home}
	repo := object{}
	for key, args := range map[string][]string{
		"branch": {"symbolic-ref", "--short", "-q", "HEAD"}, "head": {"rev-parse", "HEAD"},
		"git_directory": {"rev-parse", "--absolute-git-dir"}, "common_directory": {"rev-parse", "--git-common-dir"},
	} {
		value, _ := command(ctx, root, "git", args...)
		repo[key] = strings.TrimSpace(value)
	}
	origin, _ := command(ctx, root, "git", "remote", "get-url", "origin")
	repo["origin"] = originIdentity(strings.TrimSpace(origin))
	status, _ := command(ctx, root, "git", "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	counts := map[string]int{}
	parts := strings.Split(status, "\x00")
	for i := 0; i < len(parts); i++ {
		if len(parts[i]) < 3 {
			continue
		}
		code := parts[i][:2]
		counts[code]++
		if strings.ContainsAny(code, "RC") {
			i++
		}
	}
	repo["status_counts"] = counts
	worktrees, _ := command(ctx, root, "git", "worktree", "list", "--porcelain")
	count := 0
	for _, line := range strings.Split(worktrees, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			count++
		}
	}
	repo["worktree_count"] = count
	release, _ := command(ctx, root, "uname", "-r")
	report := object{"schema": 1, "collected_at_utc": time.Now().UTC().Format(time.RFC3339Nano),
		"cli_version": version, "platform": object{"system": runtime.GOOS, "machine": runtime.GOARCH, "release": strings.TrimSpace(release)},
		"repository": repo, "binaries": c.binaries(ctx, version), "skills": c.skills(), "hook_configs": c.hooks(), "watch_roots": c.watchRoots()}
	return c.clean(report, 0).(map[string]any), ctx.Err()
}

// Commands have bounded output and deadlines, including broken binaries and Git hooks.
func command(ctx context.Context, dir, executable string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var output limitedBuffer
	cmd.Stdout = &output
	err := cmd.Run()
	return output.String(), err
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := MaxBytes - b.Len(); remaining > 0 {
		_, _ = b.Buffer.Write(p[:min(len(p), remaining)])
	}
	return n, nil
}

func originIdentity(raw string) string {
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err == nil && parsed.Hostname() != "" {
			return parsed.Hostname() + strings.TrimSuffix(parsed.Path, ".git")
		}
	} else if match := regexp.MustCompile(`^(?:[^@/]+@)?([^:/]+):(.+)$`).FindStringSubmatch(raw); match != nil {
		return match[1] + "/" + strings.TrimSuffix(match[2], ".git")
	}
	return "[local or unrecognized remote]"
}

var urls = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)
var secrets = regexp.MustCompile(`(?i)\b(?:gh[pousr]_[a-z0-9_]+|adv_(?:cli|sa)_[a-z0-9_-]+|github_pat_[a-z0-9_]+|Bearer\s+\S+|(?:doomer_)?anon_[a-z0-9_-]{16,}|eyJ[a-z0-9_.-]{20,})`)
var homeUsers = regexp.MustCompile(`/(?:Users|home)/[^/\s"']+`)

func (c collector) clean(value any, depth int) any {
	if depth > 12 {
		return nil
	}
	switch v := value.(type) {
	case string:
		v = strings.ReplaceAll(v, c.repo, "$REPO")
		v = strings.ReplaceAll(v, c.home, "$HOME")
		v = homeUsers.ReplaceAllString(v, "/<home-user>")
		v = urls.ReplaceAllStringFunc(v, func(raw string) string {
			u, err := url.Parse(raw)
			if err != nil {
				return "[REDACTED URL]"
			}
			return u.Scheme + "://" + u.Hostname() + u.EscapedPath()
		})
		v = secrets.ReplaceAllString(v, "[REDACTED]")
		return string([]rune(v)[:min(len([]rune(v)), 2048)])
	case map[string]any:
		result := object{}
		for key, item := range v {
			result[c.clean(key, 0).(string)] = c.clean(item, depth+1)
		}
		return result
	case []any:
		result := make([]any, 0, min(len(v), 200))
		for _, item := range v[:min(len(v), 200)] {
			result = append(result, c.clean(item, depth+1))
		}
		return result
	default:
		return value
	}
}

// Refuse symlinks at every component beneath the chosen root, and cap all reads.
func readFile(root, relative string) []byte {
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
	}
	f, err := os.Open(current)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxBytes {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil || len(data) > MaxBytes {
		return nil
	}
	return data
}
func readObject(root, relative string) object {
	var value object
	_ = json.Unmarshal(readFile(root, relative), &value)
	if value == nil {
		return object{}
	}
	return value
}
func fields(value object, names ...string) object {
	result := object{}
	for _, key := range names {
		if item, ok := value[key]; ok {
			result[key] = item
		}
	}
	return result
}
func nested(value object, key string) object { child, _ := value[key].(map[string]any); return child }
func directories(root string, limit int) []string {
	entries, _ := os.ReadDir(root)
	result := []string{}
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			result = append(result, filepath.Join(root, entry.Name()))
			if len(result) == limit {
				break
			}
		}
	}
	return result
}

func (c collector) binaries(ctx context.Context, currentVersion string) []any {
	path, _ := exec.LookPath("doomer")
	self, _ := os.Executable()
	candidates := []string{self, path, filepath.Join(c.home, ".local/bin/doomer"), "/opt/homebrew/bin/doomer", "/usr/local/bin/doomer"}
	result := []any{}
	seen := map[string]bool{}
	for _, path := range candidates {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		resolved, _ := filepath.EvalSymlinks(path)
		version, err := currentVersion, error(nil)
		if path != self {
			version, err = command(ctx, c.repo, path, "version")
		}
		code := 0
		if err != nil {
			code = -1
		}
		result = append(result, object{"path": path, "resolved_path": resolved, "version_exit": code, "version": strings.TrimSpace(version)})
	}
	return result
}
func (c collector) skills() []any {
	result := []any{}
	versions := regexp.MustCompile(`20\d{2}\.\d{1,2}\.\d{1,2}\.\d+`)
	for _, relative := range []string{".agents/skills/doomer-review", ".claude/skills/doomer-review"} {
		info, err := os.Lstat(filepath.Join(c.repo, relative))
		files := []any{}
		for _, name := range []string{"SKILL.md", "references/install.md", "references/updates.md"} {
			raw := readFile(c.repo, filepath.Join(relative, name))
			if raw == nil {
				continue
			}
			sum := sha256.Sum256(raw)
			found := map[string]bool{}
			matches := []string{}
			for _, match := range versions.FindAllString(string(raw), -1) {
				if !found[match] {
					found[match] = true
					matches = append(matches, match)
				}
			}
			sort.Strings(matches)
			files = append(files, object{"file": name, "sha256": hex.EncodeToString(sum[:]), "mentioned_cli_versions": matches})
		}
		result = append(result, object{"path": relative, "exists": err == nil, "symlink": err == nil && info.Mode()&os.ModeSymlink != 0, "files": files})
	}
	return result
}
func (c collector) hooks() []any {
	result := []any{}
	roots := []string{c.home, c.repo}
	for _, root := range roots {
		for _, relative := range []string{".codex/hooks.json", ".codex/config.toml", ".claude/settings.json", ".claude/settings.local.json"} {
			result = append(result, c.hook(root, relative))
		}
	}
	if root := os.Getenv("CODEX_HOME"); root != "" {
		for _, name := range []string{"hooks.json", "config.toml"} {
			result = append(result, c.hook(root, name))
		}
	}
	if root := os.Getenv("CLAUDE_CONFIG_DIR"); root != "" {
		result = append(result, c.hook(root, "settings.json"))
	}
	return result
}

var agentCodex = regexp.MustCompile(`--agent[\\\s"']+codex`)
var agentClaude = regexp.MustCompile(`--agent[\\\s"']+claude`)

func (c collector) hook(root, name string) object {
	_, err := os.Lstat(filepath.Join(root, name))
	commands := []any{}
	data := readFile(root, name)
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(strings.ToLower(line), "doomer") || !(strings.Contains(line, "watch hook") || strings.Contains(line, "agent-hook")) {
			continue
		}
		commands = append(commands, object{"dispatch": strings.Contains(line, "--dispatch"), "agent_codex": agentCodex.MatchString(line), "agent_claude": agentClaude.MatchString(line), "suppresses_stderr": strings.Contains(line, "2>/dev/null"), "has_state": strings.Contains(line, "--state")})
		if len(commands) == 30 {
			break
		}
	}
	status := "missing"
	if len(commands) > 0 {
		status = "partial"
	}
	if strings.HasSuffix(name, ".json") && err == nil {
		managed, managedErr := watch.InstalledContent(name, data, "--managed-task-adapter")
		legacy, legacyErr := watch.InstalledContent(name, data, "watch hook")
		if managed || legacy {
			status = "installed"
		} else if managedErr != nil || legacyErr != nil {
			status = "unreadable"
		}
	} else if !strings.HasSuffix(name, ".json") {
		status = "not_applicable"
	}
	if err != nil && !os.IsNotExist(err) {
		status = "unreadable"
	}
	return object{"path": filepath.Join(root, name), "exists": err == nil, "doomer_commands": commands, "installation": status, "activation": "unverified", "repair_commands": []string{"doomer hooks install --agent codex", "doomer hooks install --agent claude"}}
}

func processExists(value any) bool {
	pid, ok := value.(float64)
	if !ok || pid <= 0 || pid != float64(int(pid)) {
		return false
	}
	process, err := os.FindProcess(int(pid))
	if err != nil {
		return false
	}
	defer process.Release()
	err = process.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}
