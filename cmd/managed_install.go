package cmd

import (
	"bytes"
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
	"github.com/doomerlabs/doomer/internal/runtimeversion"
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
					return fmt.Errorf("existing bootstrap differs at %s; reconcile it before installing", path)
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
			home, err := os.UserHomeDir()
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
		for _, path := range destinations {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return err
			}
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"state": "bootstrap_installed", "paths": destinations, "next": "Run doomer prepare in the target checkout. Approve managed updates, account login, and repository uploads separately."})
	}
	return c
}

func bindManagedSession(s managed.Store, t managed.Task) error {
	if t.Capabilities.SessionID == "" {
		return nil
	}
	path := filepath.Join(s.Root, "sessions", watch.Key(t.API+"\x00"+t.Profile+"\x00"+t.Agent+"\x00"+t.Repo+"\x00"+t.Capabilities.SessionID)+".json")
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

func newManagedHooksCommand(app *application.App, api, profile *string) *cobra.Command {
	var agent string
	c := &cobra.Command{Use: "hooks", Short: "Install the thin managed task hook adapter", Args: cobra.NoArgs}
	c.Flags().StringVar(&agent, "agent", "codex", "codex or claude")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if agent != "codex" && agent != "claude" {
			return fmt.Errorf("agent must be codex or claude")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		executable := filepath.Join(home, ".local/bin/doomer")
		command := watch.Quote(executable) + " --profile " + watch.Quote(*profile) + " --api-url " + watch.Quote(*api) + " agent-hook --agent " + agent + " --managed-task-adapter"
		if err = watch.Install(watch.HookFile(home, agent), command, "--managed-task-adapter", false); err != nil {
			return err
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"state": "hooks_configured", "hook_activated": false, "action": "Complete the agent's hook trust controls. Bind each task with prepare --session using that agent's actual session ID."})
	}
	return c
}

func newManagedAgentHookCommand(app *application.App, api, profile *string) *cobra.Command {
	var agent string
	var marker bool
	c := &cobra.Command{Use: "agent-hook", Hidden: true, Args: cobra.NoArgs}
	c.Flags().StringVar(&agent, "agent", "codex", "agent")
	c.Flags().BoolVar(&marker, "managed-task-adapter", false, "adapter ownership marker")
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
		s, err := managedStore()
		if err != nil {
			return err
		}
		path := filepath.Join(s.Root, "sessions", watch.Key(*api+"\x00"+*profile+"\x00"+agent+"\x00"+repo+"\x00"+event.Session)+".json")
		var handle string
		if err = watch.Read(path, &handle); os.IsNotExist(err) {
			return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event.Event, "additionalContext": "Doomer task is not prepared. Read the installed bootstrap and run doomer prepare --agent " + agent + " --session " + watch.Quote(event.Session) + " --repo " + watch.Quote(repo) + " before editing."}})
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
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		binary := runtimeversion.Binary(home, task.Release)
		command := exec.CommandContext(c.Context(), binary, "--api-url", *api, "--profile", *profile, "task", "hook", handle)
		command.Stdin = bytes.NewReader(data)
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = io.Discard
		err = command.Run()
		context := "Doomer task " + handle + ". Use doomer task status " + handle + " and record every finding response before finishing."
		if err != nil {
			context += " Hook operation failed; use the resumable per-call workflow."
		} else {
			context += "\n" + strings.TrimSpace(output.String())
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event.Event, "additionalContext": context}})
	}
	return c
}
