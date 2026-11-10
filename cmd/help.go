package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Render the command tree itself so help stays current as commands and flags
// change. Plain Markdown is also readable when captured by an agent tool call.
func writeMarkdownHelp(command *cobra.Command, _ []string) {
	out := command.OutOrStdout()
	command.InitDefaultHelpFlag()
	fmt.Fprintf(out, "# %s\n\n", command.CommandPath())
	description := command.Long
	if description == "" {
		description = command.Short
	}
	if description != "" {
		fmt.Fprintln(out, strings.TrimSpace(description)+"\n")
	}
	fmt.Fprintf(out, "## Usage\n\n```sh\n%s\n```\n\n", command.UseLine())
	if len(command.Aliases) > 0 {
		fmt.Fprintf(out, "Aliases: `%s`\n\n", strings.Join(command.Aliases, "`, `"))
	}
	if command.Example != "" {
		fmt.Fprintf(out, "## Examples\n\n```sh\n%s\n```\n\n", strings.TrimSpace(command.Example))
	}
	writeHelpFlags(out, "Flags", command.LocalFlags())
	writeHelpFlags(out, "Global flags", command.InheritedFlags())
	if command.HasSubCommands() {
		fmt.Fprint(out, "## Commands\n\n")
		writeHelpCommands(out, command)
		fmt.Fprintln(out, "\nAppend `--help` to any command for its arguments, flags, and input details.")
	}
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	return strings.ReplaceAll(value, "\n", "<br>")
}

func writeHelpFlags(out io.Writer, title string, flags *pflag.FlagSet) {
	if !flags.HasFlags() {
		return
	}
	fmt.Fprintf(out, "## %s\n\n| Flag | Type | Default | Required | Description |\n| --- | --- | --- | --- | --- |\n", title)
	flags.VisitAll(func(flag *pflag.Flag) {
		name := "--" + flag.Name
		if flag.Shorthand != "" {
			name += ", -" + flag.Shorthand
		}
		required := "no"
		if len(flag.Annotations[cobra.BashCompOneRequiredFlag]) > 0 {
			required = "yes"
		}
		description := flag.Usage
		if flag.Deprecated != "" {
			description += " (deprecated: " + flag.Deprecated + ")"
		}
		if flag.Hidden {
			description += " (internal)"
		}
		fmt.Fprintf(out, "| `%s` | `%s` | `%s` | %s | %s |\n", markdownCell(name), flag.Value.Type(), markdownCell(flag.DefValue), required, markdownCell(description))
	})
	fmt.Fprintln(out)
}

func writeHelpCommands(out io.Writer, parent *cobra.Command) {
	for _, child := range parent.Commands() {
		description := child.Short
		if child.Hidden {
			description += " (internal)"
		}
		fmt.Fprintf(out, "- `%s` — %s\n", child.CommandPath(), description)
		writeHelpCommands(out, child)
	}
}

func configureCommandHelp(root *cobra.Command) {
	root.Long = `Use Doomer to prepare, submit, and complete code reviews from a coding agent.
Start with preflight and prepare for the active agent and repository. Keep the
returned task handle and follow the JSON state and action fields. Use task
commands to submit source, read Quick findings, record responses, and finish.

Each command's --help provides Markdown usage and flag details. The command
index includes internal integration commands, marked internal.`
	for path, description := range map[string]string{
		"agent-hook":        "Process an installed agent lifecycle hook from JSON on stdin",
		"bootstrap status":  "Inspect installed bootstrap wrappers and available revisions",
		"bootstrap upgrade": "Apply an offered bootstrap wrapper upgrade with explicit acceptance",
		"bootstrap defer":   "Save a deferral of the current bootstrap revision in the selected scope",
		"runtime-info":      "Print runtime protocol capabilities and the instruction digest as JSON",
		"watch hook":        "Process watcher lifecycle hook JSON from stdin",
		"watch hypotheses":  "Read edit hypotheses from stdin for a single-use watcher request",
		"watch respond":     "Read finding responses from stdin for a turn-scoped watcher request",
		"watch supervise":   "Run the managed background watcher supervisor for the supplied session",
	} {
		command, _, _ := root.Find(strings.Fields(path))
		command.Short = description
	}
	descriptions := map[string]string{
		"submit":             "Upload the current source and start review for the existing task",
		"status":             "Refresh Quick review readiness, findings, and response delivery state",
		"respond":            "Read a JSON response array from stdin and deliver feedback for Quick findings",
		"finish":             "Verify current source and acknowledged Quick responses before completing the task",
		"consent":            "Record explicitly approved source-upload consent for the displayed scope",
		"revoke":             "Revoke source-upload consent for this repository or the selected integration",
		"login":              "Continue or wait for the task's browser authentication and approval",
		"hook":               "Process an agent lifecycle hook for this task using JSON on stdin",
		"pause":              "Pause automatic review activity for this task",
		"resume":             "Resume automatic review activity for this task",
		"rollback":           "Record the prior verified release for future tasks",
		"retry":              "Retry a failed review submission using the existing task",
		"repair":             "Restore the exact runtime release selected for this task",
		"context":            "Read a task summary from stdin before the first submission (maximum 4000 characters)",
		"codex-policy":       "Apply or decline the displayed scoped Codex policy proposal after the user's decision",
		"claude-permissions": "Apply or decline the displayed scoped Claude permissions after the user's decision",
		"hook-setup":         "Apply or decline the displayed hook installation proposal after the user's decision",
	}
	task, _, _ := root.Find([]string{"task"})
	for _, command := range task.Commands() {
		command.Short = descriptions[command.Name()]
		command.Long = command.Short + ". Use the HANDLE returned by prepare and the same --profile and --api-url. Preserve the handle across retries and resumed sessions."
	}
	respond, _, _ := root.Find([]string{"task", "respond"})
	respond.Long = `Read one JSON array from stdin and deliver a response for each referenced Quick finding.
Use the HANDLE returned by prepare and the same --profile and --api-url.
Inspect the source before choosing a disposition. Copy each full 64-character
lead ID from the task's Quick results. Each lead may appear once per array.

## JSON input

| Field | Type | Meaning |
| --- | --- | --- |
| lead_id | string | Full 64-character ID of a lead in the current Quick review |
| status | string | fixed, dismissed, or deferred |
| agrees | boolean or null | fixed requires true; dismissed requires false; deferred accepts true, false, or null |
| reason | string | Nonempty concrete evidence, at most 2000 bytes |

Omit the output-only sent field. Unknown fields and trailing JSON are rejected.

## Result and next action

The command returns task JSON with response delivery state. Verify sent=true
for each response before claiming it was recorded. An empty array retries pending
delivery. After changing source, run task submit again on the same handle.
Once the latest Quick review and all responses are ready, run task finish.`
	respond.Example = `doomer task respond HANDLE <<'JSON'
[
  {
    "lead_id": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    "status": "fixed",
    "agrees": true,
    "reason": "Added a nil guard before dereferencing the result; verified the failing case."
  }
]
JSON`
}
