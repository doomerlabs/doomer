package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/paths"
	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
)

type resetEntry struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Detail string `json:"detail"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	apply  func(context.Context) error
}
type resetPlan struct {
	Entries        []*resetEntry `json:"entries"`
	DryRun         bool          `json:"dry_run"`
	Complete       bool          `json:"complete"`
	states         map[string]bool
	repos          map[string]bool
	claudeRules    map[string][]string
	claudePolicies map[string][]string
}

func (p *resetPlan) add(kind, path, detail string, apply func(context.Context) error) {
	p.Entries = append(p.Entries, &resetEntry{Kind: kind, Path: path, Detail: detail, Status: "found", apply: apply})
}
func (p *resetPlan) problem(kind, path string, err error) {
	p.Entries = append(p.Entries, &resetEntry{Kind: kind, Path: path, Status: "manual_cleanup_required", Error: err.Error()})
}
func (p *resetPlan) edit(kind, path, detail string, before, after []byte, mode os.FileMode) {
	if bytes.Equal(before, after) {
		return
	}
	p.add(kind, path, detail, func(_ context.Context) error { return resetWriteFile(path, before, after, mode) })
}
func (p *resetPlan) remove(kind, path string) {
	before, mode, err := resetReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		p.problem(kind, path, err)
		return
	}
	p.add(kind, path, "remove Doomer file", func(_ context.Context) error {
		current, _, err := resetReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, before) {
			return fmt.Errorf("file changed during reset")
		}
		_ = mode
		return os.Remove(path)
	})
}

func newOnboardResetCommand() *cobra.Command {
	var dryRun, jsonOutput bool
	c := &cobra.Command{Use: "reset", Short: "Remove Doomer integrations and revoke grants across discovered installations; keep logins", Args: cobra.NoArgs,
		Long: "Discover Codex and Claude installations, known repositories, and Doomer profiles. Remove Doomer hooks, recognized instructions and reviewer policy; revoke upload, portable, and update grants. Preserve login credentials and unrelated settings. Unknown or inaccessible state is reported as incomplete. Running agents may need a restart to unload cached settings. This does not delete data already uploaded to a service.",
		RunE: func(c *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			p := discoverReset(home, os.Getenv)
			p.DryRun = dryRun
			if !jsonOutput {
				fmt.Fprintln(c.OutOrStdout(), "Discovered Doomer integrations (login credentials will be kept):")
				for _, e := range p.Entries {
					fmt.Fprintf(c.OutOrStdout(), "  %s: %s — %s\n", e.Kind, e.Path, e.Detail)
				}
			}
			if !dryRun {
				p.run(c.Context())
			}
			p.Complete = true
			for _, e := range p.Entries {
				if e.Error != "" {
					p.Complete = false
				}
			}
			if jsonOutput {
				if err := json.NewEncoder(c.OutOrStdout()).Encode(p); err != nil {
					return err
				}
			} else {
				for _, e := range p.Entries {
					if e.apply != nil || e.Error != "" {
						fmt.Fprintf(c.OutOrStdout(), "%s: %s — %s %s\n", e.Status, e.Path, e.Detail, e.Error)
					}
				}
				if dryRun {
					fmt.Fprintln(c.OutOrStdout(), "Dry run: nothing changed.")
				} else if p.Complete {
					fmt.Fprintln(c.OutOrStdout(), "Reset complete. Login credentials kept. Restart running agents to unload cached hooks and policy; onboard again to grant access.")
				}
			}
			if !p.Complete {
				return fmt.Errorf("reset incomplete; see reported cleanup errors")
			}
			return nil
		}}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show discovered installations and planned cleanup without changing files")
	c.Flags().BoolVar(&jsonOutput, "json", false, "print discovery and cleanup results as JSON")
	return c
}

func discoverReset(home string, getenv func(string) string) *resetPlan {
	p := &resetPlan{Entries: []*resetEntry{}, states: map[string]bool{}, repos: map[string]bool{}}
	installations := map[string]string{}
	add := func(kind, path string) {
		if path == "" {
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			p.problem(kind, path, err)
			return
		}
		if _, err = os.Lstat(abs); os.IsNotExist(err) {
			return
		} else if err != nil {
			p.problem(kind, abs, err)
			return
		}
		installations[abs] = kind
	}
	add("Codex", filepath.Join(home, ".codex"))
	add("Codex", getenv("CODEX_HOME"))
	add("Claude", filepath.Join(home, ".claude"))
	add("Claude", getenv("CLAUDE_CONFIG_DIR"))
	for _, family := range []struct{ pattern, agent string }{{".codex-*", "Codex"}, {".codex_*", "Codex"}, {".claude-*", "Claude"}, {".claude_*", "Claude"}} {
		matches, _ := filepath.Glob(filepath.Join(home, family.pattern))
		for _, path := range matches {
			if info, err := os.Lstat(path); err == nil && info.IsDir() {
				add(family.agent, path)
			}
		}
	}

	// Account-manager profiles contain independent Codex homes. No whole-disk scan.
	bases := []string{filepath.Join(home, "Library", "Application Support", "Codex Accounts"), filepath.Join(home, ".config", "Codex Accounts")}
	for _, env := range []string{"XDG_CONFIG_HOME", "APPDATA"} {
		if b := getenv(env); b != "" {
			bases = append(bases, filepath.Join(b, "Codex Accounts"))
		}
	}
	for _, base := range bases {
		matches, err := filepath.Glob(filepath.Join(base, "profiles", "*", "home"))
		if err != nil {
			p.problem("discovery", base, err)
		}
		for _, m := range matches {
			add("Codex", m)
		}
		add("Codex", filepath.Join(base, "original-runtime"))
	}
	dirs := []string{getenv("DOOMER_CONFIG_DIR")}
	if dir, err := paths.ConfigDir(); err == nil {
		dirs = append(dirs, dir)
	} else {
		p.problem("Doomer", home, err)
	}
	// Profile homes can also contain independent Doomer state.
	for path := range installations {
		dirs = append(dirs, filepath.Join(path, ".config", "doomer"), filepath.Join(path, "Library", "Application Support", "doomer"))
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			p.problem("Doomer", dir, err)
			continue
		}
		if p.states[abs] {
			continue
		}
		if _, err := os.Lstat(abs); os.IsNotExist(err) {
			continue
		}
		p.states[abs] = true
	}
	sorted := []string{}
	for path := range p.states {
		sorted = append(sorted, path)
	}
	sort.Strings(sorted)
	for _, path := range sorted {
		p.discoverState(path)
	}
	sorted = nil
	for path := range installations {
		sorted = append(sorted, path)
	}
	sort.Strings(sorted)
	for _, path := range sorted {
		p.discoverAgent(installations[path], path)
	}
	p.repos[home] = true
	for path := range installations {
		p.repos[path] = true
	}
	sorted = nil
	for repo := range p.repos {
		sorted = append(sorted, repo)
	}
	sort.Strings(sorted)
	for _, repo := range sorted {
		p.discoverInstructions(repo)
	}
	return p
}

func (p *resetPlan) discoverAgent(agent, home string) {
	p.add(agent, home, "installation discovered; credentials preserved", nil)
	if agent == "Codex" {
		p.discoverSkill(filepath.Join(home, "skills/doomer-review/SKILL.md"))
	}
	hookFile := filepath.Join(home, "hooks.json")
	if agent == "Claude" {
		hookFile = filepath.Join(home, "settings.json")
	}
	identities := []string{}
	hookFiles := []string{hookFile}
	if agent == "Claude" {
		hookFiles = append(hookFiles, filepath.Join(home, "settings.local.json"))
	}
	for _, file := range hookFiles {
		data, mode, err := resetReadFile(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			p.problem(agent, file, err)
			continue
		}
		var obj map[string]any
		if err = json.Unmarshal(data, &obj); err != nil {
			p.problem(agent, file, err)
			continue
		}
		n := 0
		if owned := p.claudePolicies[file]; len(owned) > 0 {
			if auto, ok := obj["autoMode"].(map[string]any); ok {
				if allow, ok := auto["allow"].([]any); ok {
					kept := []any{}
					for _, entry := range allow {
						text, _ := entry.(string)
						remove := false
						for _, policy := range owned {
							if text == policy {
								remove = true
							}
						}
						if remove {
							n++
						} else {
							kept = append(kept, entry)
						}
					}
					auto["allow"] = kept
				}
			}
		}
		if rules := p.claudeRules[file]; len(rules) > 0 {
			if permissions, ok := obj["permissions"].(map[string]any); ok {
				if allow, ok := permissions["allow"].([]any); ok {
					kept := []any{}
					for _, value := range allow {
						rule, _ := value.(string)
						owned := false
						for _, candidate := range rules {
							if rule == candidate {
								owned = true
							}
						}
						if owned {
							n++
						} else {
							kept = append(kept, value)
						}
					}
					permissions["allow"] = kept
				}
			}
		}
		hooks, ok := obj["hooks"].(map[string]any)
		if !ok && obj["hooks"] != nil {
			p.problem(agent, file, fmt.Errorf("unsupported hooks layout"))
			continue
		}
		if ok {
			ids, count, err := resetHookGroups(hooks, file)
			if err != nil {
				p.problem(agent, file, err)
				continue
			}
			identities = append(identities, ids...)
			n += count
		}
		if n > 0 {
			out, err := json.MarshalIndent(obj, "", "  ")
			if err != nil {
				p.problem(agent, file, err)
				continue
			}
			p.edit("hooks", file, fmt.Sprintf("remove %d Doomer hooks and owned command rules", n), data, append(out, '\n'), mode)
		}
	}
	if agent != "Codex" {
		return
	}
	file := filepath.Join(home, "config.toml")
	data, mode, err := resetReadFile(file)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		p.problem(agent, file, err)
		return
	}
	var obj map[string]any
	if err = toml.Unmarshal(data, &obj); err != nil {
		p.problem(agent, file, err)
		return
	}
	out := data
	count := 0
	if hooks, ok := obj["hooks"].(map[string]any); ok {
		ids, n, e := resetHookGroups(hooks, file)
		if e != nil {
			p.problem(agent, file, e)
			return
		}
		identities = append(identities, ids...)
		count += n
		if state, ok := hooks["state"].(map[string]any); ok {
			for _, id := range identities {
				if _, ok := state[id]; ok {
					delete(state, id)
					count++
				}
			}
		}
		if count > 0 {
			out, err = resetPatchTable(out, "hooks", hooks)
			if err != nil {
				p.problem(agent, file, err)
				return
			}
		}
	}
	policy, err := codexExtraPolicy(out)
	if err != nil {
		p.problem(agent, file, err)
		return
	}
	remaining, n := resetRemovePolicies(policy)
	if n > 0 {
		out, err = patchCodexExtraPolicy(out, remaining)
		if err != nil {
			p.problem(agent, file, err)
			return
		}
		count += n
	}
	if strings.Contains(remaining, "Doomer repository review authorization") {
		p.problem("reviewer policy", file, fmt.Errorf("unrecognized Doomer policy requires manual cleanup"))
	}
	if count > 0 {
		p.edit("Codex settings", file, fmt.Sprintf("remove %d Doomer hooks, trust records and reviewer grants", count), data, out, mode)
	}
}

func (p *resetPlan) discoverState(root string) {
	if err := resetCheckDirectory(root); err != nil {
		p.problem("Doomer", root, err)
		delete(p.states, root)
		return
	}
	p.add("Doomer", root, "all saved profiles; credentials preserved", nil)
	state := filepath.Join(root, "managed")
	if _, err := os.Lstat(state); err == nil {
		if err = resetCheckDirectory(state); err != nil {
			p.problem("Doomer", state, err)
			delete(p.states, root)
			return
		}
	}
	ruleFiles, _ := filepath.Glob(filepath.Join(state, "claude-permissions", "*.json"))
	for _, file := range ruleFiles {
		data, _, err := resetReadFile(file)
		var receipt claudePermissionReceipt
		if err != nil || json.Unmarshal(data, &receipt) != nil {
			p.problem("Claude permissions", file, fmt.Errorf("invalid permission ownership receipt"))
			continue
		}
		currentPath, pathErr := claudePermissionPath()
		if pathErr != nil || receipt.ConfigPath != currentPath {
			p.problem("Claude permissions", receipt.ConfigPath, fmt.Errorf("settings path does not match the current home; manually review and remove the recorded Doomer rules %q (ownership receipt: %s)", receipt.Added, file))
			continue
		}
		if p.claudeRules == nil {
			p.claudeRules = map[string][]string{}
		}
		for _, rule := range receipt.Added {
			for _, known := range claudeReviewRules {
				if rule == known {
					p.claudeRules[receipt.ConfigPath] = append(p.claudeRules[receipt.ConfigPath], rule)
				}
			}
		}
		for _, policy := range receipt.AddedPolicies {
			if recognizedClaudeClassifierPolicy(policy) {
				if p.claudePolicies == nil {
					p.claudePolicies = map[string][]string{}
				}
				p.claudePolicies[receipt.ConfigPath] = append(p.claudePolicies[receipt.ConfigPath], policy)
			}
		}
	}
	taskFiles, _ := filepath.Glob(filepath.Join(state, "tasks", "*.json"))
	for _, file := range taskFiles {
		data, mode, err := resetReadFile(file)
		if err != nil {
			p.problem("task", file, err)
			continue
		}
		var task managed.Task
		if err = json.Unmarshal(data, &task); err != nil {
			p.problem("task", file, err)
			continue
		}
		if task.Repo != "" {
			p.repos[task.Repo] = true
		}
		if task.RepositoryID != "" {
			p.discoverPortable(task.RepositoryID)
		}
		if !task.Paused || task.HookActivated || task.PortableApproved {
			task.Paused = true
			task.HookActivated = false
			task.PortableApproved = false
			after, _ := json.MarshalIndent(task, "", "  ")
			p.edit("task", file, "pause task and clear activation", data, after, mode)
		}
	}
	for _, category := range []string{"consents", "integration-consents", "update-authorizations", "selections"} {
		files, _ := filepath.Glob(filepath.Join(state, category, "*.json"))
		for _, file := range files {
			data, mode, err := resetReadFile(file)
			if err != nil {
				p.problem(category, file, err)
				continue
			}
			var obj map[string]any
			if err = json.Unmarshal(data, &obj); err != nil {
				p.problem(category, file, err)
				continue
			}
			changed := false
			for _, key := range []string{"approved", "authorized"} {
				if v, ok := obj[key].(bool); ok && v {
					obj[key] = false
					changed = true
				}
			}
			if category == "integration-consents" {
				obj["revocation_id"], err = managed.NewHandle()
				if err != nil {
					p.problem(category, file, err)
					continue
				}
				obj["source"] = "onboarding_reset"
				changed = true
			}
			if changed {
				after, _ := json.MarshalIndent(obj, "", "  ")
				p.edit(category, file, "revoke saved permission", data, after, mode)
			}
		}
	}
	for _, category := range []string{"sessions", "codex-policy-offers", "bootstrap-deferrals", "approvals"} {
		files, _ := filepath.Glob(filepath.Join(state, category, "*"))
		for _, file := range files {
			p.remove(category, file)
		}
	}
	profiles, _ := filepath.Glob(filepath.Join(root, "watch", "*"))
	for _, profile := range profiles {
		file := filepath.Join(profile, "config.json")
		data, _, err := resetReadFile(file)
		if err == nil {
			var cfg watch.Config
			if err = json.Unmarshal(data, &cfg); err == nil {
				for _, repo := range cfg.Directories {
					p.repos[repo] = true
				}
			}
			if err != nil {
				p.problem("watch profile", file, err)
			}
		} else if !os.IsNotExist(err) {
			p.problem("watch profile", file, err)
		}
		p.remove("watch consent", file)
		// Pause every generation before clearing consent. Retain history and credentials.
		roots := []string{profile, filepath.Join(profile, "supervisor")}
		runtimes, _ := filepath.Glob(filepath.Join(profile, "runtimes", "*"))
		roots = append(roots, runtimes...)
		for _, r := range roots {
			if info, err := os.Lstat(r); err != nil || !info.IsDir() {
				continue
			}
			for _, category := range []string{"checkouts", "discovered"} {
				records, _ := filepath.Glob(filepath.Join(r, category, "*.json"))
				for _, file := range records {
					data, _, err := resetReadFile(file)
					if err != nil {
						p.problem("watch repository", file, err)
						continue
					}
					var repo string
					if category == "checkouts" {
						var record watch.Repository
						err = json.Unmarshal(data, &record)
						repo = record.Path
					} else {
						err = json.Unmarshal(data, &repo)
					}
					if err != nil {
						p.problem("watch repository", file, err)
					} else if repo != "" {
						p.repos[repo] = true
					}
				}
			}
			for _, category := range []string{"task-handoffs", "onboarding", "activations", "hook-diagnostics", "turns"} {
				files, _ := filepath.Glob(filepath.Join(r, category, "*.json"))
				for _, file := range files {
					p.remove("onboarding state", file)
				}
			}
			r := r
			p.add("watcher", r, "stop background watcher", func(ctx context.Context) error {
				lock, err := lifecycleLock(ctx, r)
				if err != nil {
					return err
				}
				defer lock.Close()
				if err = watch.Write(filepath.Join(r, "paused.json"), time.Now()); err != nil {
					return err
				}
				return waitWatch(ctx, func() (bool, error) { held, err := publock.Held(r, "watcher"); return !held, err })
			})
		}
	}
}

func (p *resetPlan) discoverPortable(id string) {
	files, _ := filepath.Glob(filepath.Join(id, "doomer", "portable", "*.json"))
	for _, file := range files {
		for _, e := range p.Entries {
			if e.Path == file {
				return
			}
		}
		data, mode, err := resetReadFile(file)
		if err != nil {
			p.problem("portable grant", file, err)
			continue
		}
		var record portableSetup
		if err = json.Unmarshal(data, &record); err != nil {
			p.problem("portable grant", file, err)
			continue
		}
		if record.Grant.Approved || record.Authorized {
			record.Grant.Approved = false
			record.Authorized = false
			after, _ := json.MarshalIndent(record, "", "  ")
			p.edit("portable grant", file, "revoke portable permissions; preserve credential", data, after, mode)
		}
	}
}
func (p *resetPlan) discoverInstructions(repo string) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		file := filepath.Join(repo, name)
		data, mode, err := resetReadFile(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			p.problem("instructions", file, err)
			continue
		}
		const start = "<!-- doomer-review:start -->"
		const end = "<!-- doomer-review:end -->"
		text := string(data)
		i, j := strings.Index(text, start), strings.Index(text, end)
		if i < 0 && j < 0 {
			continue
		}
		if i < 0 || j < i || strings.Count(text, start) != 1 || strings.Count(text, end) != 1 {
			p.problem("instructions", file, fmt.Errorf("malformed Doomer instruction markers"))
			continue
		}
		p.edit("instructions", file, "remove marked Doomer instructions", data, []byte(text[:i]+text[j+len(end):]), mode)
	}
	for _, name := range []string{".agents/skills/doomer-review/SKILL.md", ".claude/skills/doomer-review/SKILL.md"} {
		p.discoverSkill(filepath.Join(repo, name))
	}
	// Repository-level agent hooks also belong to known installations.
	if repo != os.Getenv("HOME") {
		for _, name := range []string{".claude", ".codex"} {
			if info, err := os.Lstat(filepath.Join(repo, name)); err == nil && info.IsDir() {
				agent := "Codex"
				if name == ".claude" {
					agent = "Claude"
				}
				p.discoverAgent(agent, filepath.Join(repo, name))
			}
		}
	}
}

func (p *resetPlan) run(ctx context.Context) {
	if err := ctx.Err(); err != nil {
		p.problem("reset", "", err)
		return
	}
	var locks []*publock.Lock
	defer func() {
		for _, l := range locks {
			l.Close()
		}
	}()
	// Hold every existing task lock so in-flight uploads finish before revocation.
	// The marker blocks new preparations while the reset is executing.
	var markers []string
	defer func() {
		for _, m := range markers {
			_ = os.Remove(m)
		}
	}()
	for root := range p.states {
		state := filepath.Join(root, "managed")
		if _, err := os.Lstat(state); os.IsNotExist(err) {
			continue
		}
		lock, err := publock.TryAcquire(state, "onboarding-reset")
		if err != nil {
			p.problem("reset lock", state, err)
			return
		}
		locks = append(locks, lock)
		marker := filepath.Join(state, "reset-in-progress")
		// The OS lock proves any prior marker is stale after an interrupted reset.
		if _, _, err := resetReadFile(marker); err != nil && !os.IsNotExist(err) {
			p.problem("reset lock", marker, err)
			return
		}
		if err := watch.Write(marker, true); err != nil {
			p.problem("reset lock", marker, err)
			return
		}
		markers = append(markers, marker)
		files, _ := filepath.Glob(filepath.Join(state, "tasks", "*.json"))
		for _, file := range files {
			handle := strings.TrimSuffix(filepath.Base(file), ".json")
			l, err := (managed.Store{Root: state}).Lock(handle)
			if err != nil {
				p.problem("active task", file, err)
				return
			}
			locks = append(locks, l)
		}
		// Durable cutoff also invalidates undiscovered old portable copies.
		if err := watch.Write(filepath.Join(state, "reset.json"), time.Now().UTC()); err != nil {
			p.problem("reset cutoff", state, err)
			return
		}
	}
	// Stop watchers first, before modifying the rest of their state.
	for _, kind := range []string{"watcher", "other"} {
		for _, e := range p.Entries {
			if e.apply == nil || (e.Kind == "watcher") != (kind == "watcher") {
				continue
			}
			if err := ctx.Err(); err != nil {
				e.Status = "failed"
				e.Error = err.Error()
				continue
			}
			if err := e.apply(ctx); err != nil {
				e.Status = "failed"
				e.Error = err.Error()
			} else {
				e.Status = "cleared"
			}
		}
	}
}

// Old portable grants must not resurrect permission after a local reset.
func resetCutoff(s managed.Store) (time.Time, error) {
	var cutoff time.Time
	err := watch.Read(filepath.Join(s.Root, "reset.json"), &cutoff)
	if os.IsNotExist(err) {
		return cutoff, nil
	}
	if err != nil {
		return cutoff, fmt.Errorf("read onboarding reset record: %w", err)
	}
	return cutoff, nil
}

func (p *resetPlan) discoverSkill(file string) {
	data, _, err := resetReadFile(file)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		p.problem("skill", file, err)
		return
	}
	b, err := managed.ReadBootstrap(file, "personal")
	if err != nil || !managed.RecognizedBootstrap(data, b.Policy) {
		p.problem("skill", file, fmt.Errorf("custom Doomer skill preserved; remove manually if desired"))
		return
	}
	p.remove("skill", file)
}
