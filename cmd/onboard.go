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
	c.Flags().StringVar(&session, "session", os.Getenv("CODEX_THREAD_ID"), "current coding-agent session")
	c.Flags().BoolVar(&taskStdin, "task-stdin", false, "read the original task from stdin for a required restart")
	c.Flags().BoolVar(&restartRequired, "restart", false, "the CLI was installed during this turn and needs a fresh prompt capture")
	c.RunE = func(c *cobra.Command, _ []string) error {
		if agent != "codex" && agent != "claude" {
			return fmt.Errorf("--agent must be codex or claude")
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
		if err := watching.EnsureAgentInstructions(path); err != nil {
			return err
		}
		if restart && agent == "codex" {
			message := "Your conversation stays in Codex."
			if taskStdin {
				message = "Your task is saved."
			}
			fmt.Fprintf(c.OutOrStdout(), "\nOne restart left. %s\n1. Copy the command below.\n2. Exit Codex with Ctrl+C until you reach your terminal.\n3. Paste and run:\n", message)
			resume := "--last"
			if session != "" {
				resume = watching.Quote(session)
			}
			fmt.Fprintf(c.OutOrStdout(), "\ncd %s && codex resume %s %s\n\n", watching.Quote(path), resume, watching.Quote("Resume after doomer.ai setup"))
			fmt.Fprintln(c.OutOrStdout(), "Review and trust the Doomer hooks when prompted. The watcher is already running; the trusted hook supplies your saved task. Do not run setup again after resuming.")
		} else if restart {
			fmt.Fprintln(c.OutOrStdout(), "Restart your coding agent to load the new Doomer hooks. The watcher is already running.")
		} else {
			fmt.Fprintln(c.OutOrStdout(), "Doomer is configured and the watcher is running. Hook trust remains a user choice; continue your task.")
		}
		return nil
	}
	return c
}
