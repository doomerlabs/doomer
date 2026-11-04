package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/doomerlabs/doomer/internal/application"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

// Onboarding uses the same consent and background-start path as watch start.
// There is no separate grant, implicit resume, or hook-trust bypass.
func newOnboardCommand(app *application.App, api, profile *string) *cobra.Command {
	var repo, agent, session string
	var taskStdin, restartRequired, checkHooks, refresh bool
	c := &cobra.Command{Use: "onboard", Short: "Set up repository reviews and preserve a task across hook installation", Args: cobra.NoArgs}
	c.Flags().StringVar(&repo, "repo", ".", "repository to review")
	c.Flags().StringVar(&agent, "agent", "codex", "coding agent: codex or claude")
	c.Flags().StringVar(&session, "session", "", "current coding-agent session (Codex defaults to CODEX_THREAD_ID)")
	c.Flags().BoolVar(&taskStdin, "task-stdin", false, "read the original task from stdin for hook activation or restart")
	c.Flags().BoolVar(&refresh, "refresh", false, "gracefully refresh a running managed watcher after a CLI update")
	c.Flags().BoolVar(&restartRequired, "restart", false, "Codex: the CLI was installed during this turn and needs fresh prompt capture")
	c.Flags().BoolVar(&checkHooks, "check-hooks", false, "verify live Codex or Claude hooks without changing setup")
	c.MarkFlagsMutuallyExclusive("check-hooks", "task-stdin")
	c.MarkFlagsMutuallyExclusive("check-hooks", "restart")
	c.MarkFlagsMutuallyExclusive("check-hooks", "refresh")
	c.MarkFlagsMutuallyExclusive("refresh", "restart")
	c.MarkFlagsMutuallyExclusive("refresh", "task-stdin")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if agent != "codex" && agent != "claude" {
			return fmt.Errorf("--agent must be codex or claude")
		}
		if !c.Flags().Changed("session") && agent == "codex" {
			session = os.Getenv("CODEX_THREAD_ID")
		}
		path, err := watching.Root(c.Context(), repo)
		if err != nil {
			return err
		}
		if refresh {
			if taskStdin || restartRequired {
				return fmt.Errorf("--refresh cannot be combined with --task-stdin or --restart")
			}
			before, err := inspectWatch(app, *api, *profile, path)
			if err != nil {
				return err
			}
			if !before.Approved {
				return fmt.Errorf("refresh requires existing approval; run normal onboarding first")
			}
			root, _, err := watchPaths(*api, *profile)
			if err != nil {
				return err
			}
			refreshed, err := refreshManagedWatch(c.Context(), root, func(previous watchProcess) error {
				return startManagedWatch(c.Context(), root, previous.WorkingDirectory, *api, *profile, previous.Directories)
			})
			if err != nil {
				return err
			}
			status := "not_running"
			if refreshed {
				status = "refreshed"
			}
			return json.NewEncoder(c.OutOrStdout()).Encode(map[string]string{"status": status})
		}
		if checkHooks {
			if session == "" {
				return fmt.Errorf("--check-hooks requires a current session ID")
			}
			root, _, err := watchPaths(*api, *profile)
			if err != nil {
				return err
			}
			status, err := inspectWatch(app, *api, *profile, path)
			if err != nil {
				return err
			}
			if agent == "codex" {
				result, detail := watching.CodexActivationStatus(root, path, session)
				if !status.Approved || status.Paused || !status.Running || !status.HooksInstalled[agent] || status.RepositoryStatus != "Watching" {
					result = "error"
					detail = fmt.Sprintf("Repository setup unavailable (approved=%t, paused=%t, hooks_installed=%t, watcher_running=%t, repository_status=%q)", status.Approved, status.Paused, status.HooksInstalled[agent], status.Running, status.RepositoryStatus)
				}
				return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"status": result, "detail": detail, "repository": path, "session": session})
			}
			result := watching.ClaudeActivationStatus(root, path, session)
			if !status.Approved || status.Paused || !status.Running || !status.HooksInstalled[agent] {
				result = "setup_unavailable"
			}
			return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"status": result})
		}
		var task string
		if taskStdin {
			raw, err := io.ReadAll(io.LimitReader(c.InOrStdin(), 16001))
			if err != nil {
				return err
			}
			task = strings.TrimSpace(string(raw))
			if session == "" || len(session) > 256 || task == "" || len(raw) > 16000 || len([]rune(task)) > 3000 {
				return fmt.Errorf("task requires a session and 1–3000 characters of original request")
			}
		}

		before, err := inspectWatch(app, *api, *profile, path)
		if err != nil {
			return err
		}
		watch := newWatchCommand(app, api, profile)
		start, _, err := watch.Find([]string{"start"})
		if err != nil {
			return err
		}
		start.SetContext(c.Context())
		start.SetIn(c.InOrStdin())
		start.SetOut(c.OutOrStdout())
		start.SetErr(c.ErrOrStderr())
		if err := start.ParseFlags([]string{"--agent=" + agent, "--browser"}); err != nil {
			return err
		}
		if err := start.RunE(start, []string{path}); err != nil {
			return err
		}
		desktop := agent == "codex" && codexDesktopSession()
		restart := (restartRequired || !before.HooksInstalled[agent]) && !desktop
		if taskStdin {
			root, _, err := watchPaths(*api, *profile)
			if err != nil {
				return err
			}
			if err := watching.SaveTaskHandoff(root, path, session, strings.NewReader(task)); err != nil {
				return err
			}
		}
		if err := watching.EnsureAgentInstructions(path, agent); err != nil {
			return err
		}
		if agent == "claude" && session != "" {
			root, _, err := watchPaths(*api, *profile)
			if err != nil {
				return err
			}
			if err := watching.BeginClaudeActivation(root, path, session, task); err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), "\nClaude can load these hooks without restarting. After this command returns, verify actual hook delivery with:")
			fmt.Fprintf(c.OutOrStdout(), "%s --profile %s --api-url %s onboard --repo %s --agent claude --session %s --check-hooks\n", watching.Quote(os.Args[0]), watching.Quote(*profile), watching.Quote(*api), watching.Quote(path), watching.Quote(session))
			fmt.Fprintln(c.OutOrStdout(), "Continue when status is ready; no restart or extra trust prompt is needed. If waiting_for_hooks, check once more after the next tool result. If still unconfirmed, inspect /hooks and any workspace trust or managed-policy restrictions; do not assume a restart is required.")
			return nil
		}
		if desktop {
			fmt.Fprintln(c.OutOrStdout(), "\nStay in this Codex desktop chat. Review and approve Doomer's hooks using Review hooks beside the composer, or open Settings > Hooks if that button is not visible.")
			fmt.Fprintf(c.OutOrStdout(), "After approving, send %q in this chat so Doomer can verify live hook delivery. The watcher is running. Hook activation remains unverified until a current-session callback is received.\n", watching.ResumeHandoffPrompt)
			if taskStdin {
				fmt.Fprintln(c.OutOrStdout(), "Your task is saved. The trusted hook restores it when you send that message in this chat.")
			}
		} else if restart && agent == "codex" {
			name := "Codex"
			message := "Your conversation stays in " + name + "."
			if taskStdin {
				message = "Your task is saved."
			}
			fmt.Fprintf(c.OutOrStdout(), "\nOne restart left. %s\n1. Copy the command below.\n2. Exit %s with Ctrl+C until you reach your terminal.\n3. Paste and run:\n", message, name)
			fmt.Fprintf(c.OutOrStdout(), "\n%s\n\n", onboardingResumeCommand(path, agent, session))
			fmt.Fprintln(c.OutOrStdout(), "Review and trust the Doomer hooks when prompted. The watcher is already running; the trusted hook supplies your saved task. Do not run setup again after resuming.")
		} else if agent == "claude" {
			fmt.Fprintln(c.OutOrStdout(), "Claude hooks are configured. A session ID was not supplied, so live delivery cannot be confirmed. Inspect /hooks if feedback does not arrive; restart only if the definitions have not loaded.")
		} else {
			fmt.Fprintln(c.OutOrStdout(), "Doomer is configured and the watcher is running. Hook trust remains a user choice; continue your task.")
		}
		if agent == "codex" && session != "" {
			fmt.Fprintln(c.OutOrStdout(), "Verify live Codex hooks for this repository and session with:")
			fmt.Fprintf(c.OutOrStdout(), "%s --profile %s --api-url %s onboard --repo %s --agent codex --session %s --check-hooks\n", watching.Quote(os.Args[0]), watching.Quote(*profile), watching.Quote(*api), watching.Quote(path), watching.Quote(session))
			fmt.Fprintln(c.OutOrStdout(), "Continue when status is ready, even if the hook returned no chat message. If pending, run one read-only tool in this repository and check once more. Report any error without repeating hook approval solely because of silence.")
		}
		return nil
	}
	return c
}

func codexDesktopSession() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("CODEX_INTERNAL_ORIGINATOR_OVERRIDE")), "Codex Desktop")
}

func onboardingResumeCommand(repo, agent, session string) string {
	resume := "codex resume --last"
	if agent == "claude" {
		resume = "claude --continue"
		if session != "" {
			resume = "claude --resume " + watching.Quote(session)
		}
	} else if session != "" {
		resume = "codex resume " + watching.Quote(session)
	}
	return "cd " + watching.Quote(repo) + " && " + resume + " " + watching.Quote(watching.ResumeHandoffPrompt)
}

func hookTrustInstructions(agent string) string {
	if agent == "claude" {
		return "Claude uses workspace trust. Approve a trust prompt only if Claude shows one."
	}
	if agent == "both" {
		return "Review Doomer's hooks in Codex (Review hooks or Settings > Hooks on desktop, /hooks in the terminal) and Claude's /hooks, and approve any trust prompts yourself."
	}
	if codexDesktopSession() {
		return "In Codex desktop, select Review hooks beside the composer or open Settings > Hooks, and approve Doomer's hooks."
	}
	return "In Codex, open /hooks and trust the Doomer CLI hooks."
}
