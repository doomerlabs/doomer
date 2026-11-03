package cmd

import (
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
	var taskStdin, restartRequired bool
	c := &cobra.Command{Use: "onboard", Short: "Set up repository reviews and preserve a task across hook installation", Args: cobra.NoArgs}
	c.Flags().StringVar(&repo, "repo", ".", "repository to review")
	c.Flags().StringVar(&agent, "agent", "codex", "coding agent: codex or claude")
	c.Flags().StringVar(&session, "session", "", "current coding-agent session (Codex defaults to CODEX_THREAD_ID)")
	c.Flags().BoolVar(&taskStdin, "task-stdin", false, "read the original task from stdin for a required restart")
	c.Flags().BoolVar(&restartRequired, "restart", false, "the CLI was installed during this turn and needs a fresh prompt capture")
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
		restart := restartRequired || !before.HooksInstalled[agent]
		if restart && taskStdin {
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
		if restart {
			name := "Codex"
			if agent == "claude" {
				name = "Claude Code"
			}
			message := "Your conversation stays in " + name + "."
			if taskStdin {
				message = "Your task is saved."
			}
			fmt.Fprintf(c.OutOrStdout(), "\nOne restart left. %s\n1. Copy the command below.\n2. Exit %s with Ctrl+C until you reach your terminal.\n3. Paste and run:\n", message, name)
			fmt.Fprintf(c.OutOrStdout(), "\n%s\n\n", onboardingResumeCommand(path, agent, session))
			fmt.Fprintln(c.OutOrStdout(), "Review and trust the Doomer hooks when prompted. The watcher is already running; the trusted hook supplies your saved task. Do not run setup again after resuming.")
		} else {
			fmt.Fprintln(c.OutOrStdout(), "Doomer is configured and the watcher is running. Hook trust remains a user choice; continue your task.")
		}
		return nil
	}
	return c
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
		return "In Claude Code, review Doomer's hooks in /hooks and approve any workspace trust prompt yourself."
	}
	if agent == "both" {
		return "Review Doomer's hooks in /hooks in each coding agent and approve its trust prompts yourself."
	}
	return "In Codex, open /hooks and trust the Doomer CLI hooks."
}
