package cmd

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/selfupdate"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

func newManagedInstallCommand() *cobra.Command {
	var personal, runtimeInstall, adopt bool
	var policy, release, repo string
	var delay int
	c := &cobra.Command{Use: "install", Short: "Install stable Doomer bootstraps for Codex and Claude", Args: cobra.NoArgs}
	c.Flags().BoolVar(&runtimeInstall, "runtime", false, "install the signed runtime and shared launcher as well as bootstraps")
	c.Flags().BoolVar(&adopt, "adopt", false, "explicitly replace an unmanaged launcher, preserving a backup")
	c.Flags().BoolVar(&personal, "personal", false, "install in personal agent directories instead of this repository")
	c.Flags().StringVar(&repo, "repo", ".", "target repository")
	c.Flags().StringVar(&policy, "policy", "stable", "stable, delayed, pinned, or manual")
	c.Flags().StringVar(&release, "version", "", "exact version for pinned policy")
	c.Flags().IntVar(&delay, "delay-hours", 0, "minimum release age for delayed policy")
	c.RunE = func(c *cobra.Command, _ []string) error {
		p := managed.Policy{Protocol: managed.Protocol, Channel: policy, Version: release, DelayHours: delay}
		content, err := managed.BootstrapText(p)
		if err != nil {
			return err
		}
		base, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if !personal {
			base, err = watch.Root(c.Context(), repo)
			if err != nil {
				return err
			}
		}
		destinations := []string{filepath.Join(base, ".agents/skills/doomer-review/SKILL.md"), filepath.Join(base, ".claude/skills/doomer-review/SKILL.md")}
		// Inspect all destinations before replacing any. Unknown existing content
		// requires explicit removal by the user; never overwrite custom instructions.
		for _, path := range destinations {
			if info, err := os.Lstat(path); err == nil {
				if !info.Mode().IsRegular() {
					return fmt.Errorf("bootstrap destination is not a regular file: %s", path)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				if string(data) != content {
					return fmt.Errorf("existing bootstrap differs at %s; inspect it with doomer bootstrap status before offering an upgrade", path)
				}
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if runtimeInstall {
			catalog, err := managed.FetchCatalog(c.Context(), &http.Client{Timeout: 15 * time.Second})
			if err != nil {
				return err
			}
			selected, err := managed.SelectRelease(catalog, p, time.Now())
			if err != nil {
				return err
			}
			home, err := managedRuntimeHome()
			if err != nil {
				return err
			}
			updater := selfupdate.Updater{Home: home, Versioned: true, ExpectedManifestSHA256: selected.ManifestSHA256}
			if _, err = updater.Apply(c.Context(), selected.Version, false); err != nil {
				return err
			}
			if _, err = updater.InstallLauncher(selected.Version, adopt); err != nil {
				return err
			}
		}
		if err := installBootstrapFiles(base, destinations, []byte(content)); err != nil {
			return err
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"state": "bootstrap_installed", "paths": destinations, "next": "Run doomer prepare in the target checkout. After repository approval, configure the active agent hooks through its normal trust controls. For team installation, open the initial bootstrap PR."})
	}
	return c
}

func bindManagedSession(s managed.Store, t managed.Task) error {
	if t.Capabilities.SessionID == "" {
		return nil
	}
	key := watch.Key(t.API + "\x00" + t.Profile + "\x00" + t.Agent + "\x00" + t.Repo + "\x00" + t.Capabilities.SessionID)
	unlock, err := watchLock(s.Root, "session-"+key)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(s.Root, "sessions", key+".json")
	var old string
	if err := watch.Read(path, &old); err == nil && old != t.Handle {
		previous, err := s.Load(old)
		if err != nil {
			return err
		}
		if previous.Stage != "complete" {
			return fmt.Errorf("session already owns task %s; resume it before creating another task", old)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	return watch.Write(path, t.Handle)
}

func clearStaleManagedSessions(s managed.Store, task managed.Task) error {
	current := watch.Key(task.API + "\x00" + task.Profile + "\x00" + task.Agent + "\x00" + task.Repo + "\x00" + task.Capabilities.SessionID)
	files, err := filepath.Glob(filepath.Join(s.Root, "sessions", "*.json"))
	if err != nil {
		return err
	}
	for _, path := range files {
		key := strings.TrimSuffix(filepath.Base(path), ".json")
		if key == current {
			continue
		}
		unlock, err := watchLock(s.Root, "session-"+key)
		if err != nil {
			return err
		}
		var handle string
		err = watch.Read(path, &handle)
		if err == nil && handle == task.Handle {
			err = os.Remove(path)
		}
		unlock()
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func newManagedHooksCommand(app *application.App, api, profile *string) *cobra.Command {
	var agent string
	c := &cobra.Command{Use: "hooks", Short: "Install the thin managed task hook adapter", Args: cobra.NoArgs}
	c.Flags().StringVar(&agent, "agent", "codex", "codex or claude")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if agent != "codex" && agent != "claude" {
			return fmt.Errorf("agent must be codex or claude")
		}
		home, err := managedRuntimeHome()
		if err != nil {
			return err
		}
		executable := filepath.Join(home, ".local/bin/doomer")
		prefix := ""
		if developmentRuntime(*api, *profile) {
			executable, err = os.Executable()
			if err != nil {
				return err
			}
			state, stateErr := managedStore()
			if stateErr != nil {
				return stateErr
			}
			executable, err = stageDevelopmentBinary(executable, state.Root)
			if err != nil {
				return err
			}
			prefix = "env DOOMER_DEV_CLI=1 "
		}
		command := prefix + watch.Quote(executable) + " --profile " + watch.Quote(*profile) + " --api-url " + watch.Quote(*api) + " agent-hook --agent " + agent + " --managed-task-adapter"
		userHome, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		state, err := managedStore()
		if err != nil {
			return err
		}
		command += " --state " + watch.Quote(state.Root)
		if err = watch.Install(watch.HookFile(userHome, agent), command, "--managed-task-adapter", false); err != nil {
			return err
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"state": "hooks_configured", "hook_activated": false, "action": "Complete the agent's hook trust controls. Bind each task with prepare --session using that agent's actual session ID."})
	}
	return c
}

func newManagedAgentHookCommand(app *application.App, api, profile *string) *cobra.Command {
	var agent string
	var marker bool
	var statePath string
	c := &cobra.Command{Use: "agent-hook", Hidden: true, Args: cobra.NoArgs}
	c.Flags().StringVar(&agent, "agent", "codex", "agent")
	c.Flags().BoolVar(&marker, "managed-task-adapter", false, "adapter ownership marker")
	c.Flags().StringVar(&statePath, "state", "", "absolute managed state root recorded by hook installation")
	c.RunE = func(c *cobra.Command, _ []string) error {
		var event watch.HookInput
		data, err := io.ReadAll(io.LimitReader(c.InOrStdin(), 1<<20))
		if err != nil {
			return err
		}
		if json.Unmarshal(data, &event) != nil || event.Session == "" || event.CWD == "" {
			return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{})
		}
		repo, err := watch.Root(c.Context(), event.CWD)
		if err != nil {
			return err
		}
		var s managed.Store
		if statePath != "" {
			if !filepath.IsAbs(statePath) {
				return fmt.Errorf("hook state must be absolute")
			}
			s = managed.Store{Root: statePath}
			err = s.Init()
		} else {
			s, err = managedStore()
		}
		if err != nil {
			return err
		}
		path := filepath.Join(s.Root, "sessions", watch.Key(*api+"\x00"+*profile+"\x00"+agent+"\x00"+repo+"\x00"+event.Session)+".json")
		var handle string
		if err = watch.Read(path, &handle); os.IsNotExist(err) {
			if event.Event == "Stop" {
				return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{})
			}
			message := "Doomer task is not prepared for this session. If the user supplied an existing task handle, first resume it with doomer prepare --task HANDLE --session " + watch.Quote(event.Session) + ". Do not create a replacement task for recovery. For a new task, read the installed bootstrap and attempt doomer prepare --agent " + agent + " --session " + watch.Quote(event.Session) + " --repo " + watch.Quote(repo) + " early. Include the actual user request with --prompt."
			pending, lookupErr := pendingManagedSetup(s, *api, *profile, agent, repo)
			if lookupErr != nil {
				return lookupErr
			}
			if pending != "" {
				message = "For an installation or approval reply, continue the existing setup with doomer prepare --task " + watch.Quote(pending) + " --session " + watch.Quote(event.Session) + ". For a new coding request, start its own task with doomer prepare --agent " + agent + " --session " + watch.Quote(event.Session) + " --repo " + watch.Quote(repo) + " --prompt USER_REQUEST. Keep setup-only tasks separate from coding reviews."
			}
			message += " If Doomer is blocked, continue already-authorized local edits and tests unless the user explicitly required review first or asked to wait. Stop the denied operation, preserve host controls, and report incomplete review coverage. Do not ask whether to continue."
			return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event.Event, "additionalContext": message}})
		} else if err != nil {
			return err
		}
		task, err := s.Load(handle)
		if err != nil {
			return err
		}
		if task.Stage == "complete" {
			return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{})
		}
		// Invoke the task's exact release as a separate bounded call. No watcher
		// process needs to survive this hook invocation.
		binary, err := managedTaskBinary(task)
		if err != nil {
			return err
		}
		command := exec.CommandContext(c.Context(), binary, "--api-url", *api, "--profile", *profile, "task", "hook", handle)
		command.Stdin = bytes.NewReader(data)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "DOOMER_CONFIG_DIR=") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "DOOMER_CONFIG_DIR="+filepath.Dir(s.Root))
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = io.Discard
		err = command.Run()
		return json.NewEncoder(c.OutOrStdout()).Encode(managedHookFeedback(event.Event, output.Bytes(), err))
	}
	return c
}

// Stage complete files and publish only absent destinations. The installation
// lock coordinates both agent copies; rollback removes only our new files.
func installBootstrapFiles(base string, paths []string, content []byte) error {
	return writeBootstrapFiles(base, paths, content, nil)
}

func writeBootstrapFiles(base string, paths []string, content []byte, expected map[string][]byte) error {
	state, err := managedStore()
	if err != nil {
		return err
	}
	unlock, err := watchLock(state.Root, "managed-bootstrap-install-"+watch.Key(base))
	if err != nil {
		return err
	}
	defer unlock()
	root, err := os.OpenRoot(base)
	if err != nil {
		return err
	}
	defer root.Close()
	var created []string
	replaced := map[string][]byte{}
	success := false
	defer func() {
		if !success {
			for path, previous := range replaced {
				current, err := root.ReadFile(path)
				if err == nil && bytes.Equal(current, content) {
					_ = root.WriteFile(path, previous, 0644)
				}
			}
			for _, path := range created {
				current, err := root.ReadFile(path)
				if err == nil && bytes.Equal(current, content) {
					_ = root.Remove(path)
				}
			}
		}
	}()
	for _, path := range paths {
		relative, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		parts := strings.Split(relative, string(filepath.Separator))
		directory := ""
		for _, part := range parts[:len(parts)-1] {
			directory = filepath.Join(directory, part)
			info, err := root.Lstat(directory)
			if os.IsNotExist(err) {
				if err = root.Mkdir(directory, 0755); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("bootstrap parent is not a regular directory: %s", directory)
			}
		}
		if info, err := root.Lstat(relative); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("bootstrap destination changed")
			}
			existing, err := root.ReadFile(relative)
			if err != nil {
				return err
			}
			previous, replacing := expected[path]
			if !bytes.Equal(existing, content) && (!replacing || !bytes.Equal(existing, previous)) {
				return fmt.Errorf("bootstrap content changed during installation")
			}
			if bytes.Equal(existing, content) {
				continue
			}
			replaced[relative] = existing
		} else if !os.IsNotExist(err) {
			return err
		} else if _, present := expected[path]; present {
			return fmt.Errorf("bootstrap disappeared during upgrade")
		}
		temporary := filepath.Join(filepath.Dir(relative), ".doomer-"+rand.Text())
		file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(content)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			_ = root.Remove(temporary)
			return fmt.Errorf("stage bootstrap: %v %v", writeErr, closeErr)
		}
		if err = root.Rename(temporary, relative); err != nil {
			_ = root.Remove(temporary)
			return err
		}
		if _, replacing := replaced[relative]; !replacing {
			created = append(created, relative)
		}
	}
	success = true
	return nil
}

// Rechecks with a real session reuse its setup task instead of creating an
// orphan before the session binding rejects a replacement.
func resumableManagedSetup(s managed.Store, api, profile, agent, repo, session string) (string, error) {
	if session != "" {
		key := watch.Key(api + "\x00" + profile + "\x00" + agent + "\x00" + repo + "\x00" + session)
		var handle string
		err := watch.Read(filepath.Join(s.Root, "sessions", key+".json"), &handle)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if err == nil {
			task, err := s.Load(handle)
			if err != nil {
				return "", err
			}
			if task.API != api || task.Profile != profile || task.Agent != agent || task.Repo != repo || task.Capabilities.SessionID != session {
				return "", fmt.Errorf("session task does not match the requested setup scope")
			}
			if task.Paused {
				return "", fmt.Errorf("session task %s is paused; resume it explicitly", handle)
			}
			if task.Purpose == "setup" {
				return handle, nil
			}
			if task.Stage != "complete" {
				return "", fmt.Errorf("session already owns task %s; resume it before creating another task", handle)
			}
		}
	}
	return pendingManagedSetup(s, api, profile, agent, repo)
}

// Offer a saved handle only when one unbound setup unambiguously matches this
// hook's complete scope. Ambiguous or coding tasks retain normal preparation.
func pendingManagedSetup(s managed.Store, api, profile, agent, repo string) (string, error) {
	files, err := filepath.Glob(filepath.Join(s.Root, "tasks", "*.json"))
	if err != nil {
		return "", err
	}
	handle := ""
	for _, file := range files {
		var task managed.Task
		if err := watch.Read(file, &task); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if task.API != api || task.Profile != profile || task.Agent != agent || task.Repo != repo || task.Purpose != "setup" || task.Paused || task.Stage == "complete" || task.Capabilities.SessionID != "" {
			continue
		}
		if handle != "" {
			return "", nil
		}
		handle = task.Handle
	}
	return handle, nil
}
