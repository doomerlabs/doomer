package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// A filesystem refusal is not evidence that the user or approval reviewer
// denied a request. Keep the original error/exit status and supply recovery
// context without claiming authentication, consent, or task creation succeeded.
type managedStateAccess struct {
	State                 string            `json:"state"`
	Error                 string            `json:"error"`
	Operation             string            `json:"operation"`
	Path                  string            `json:"path"`
	Task                  string            `json:"task,omitempty"`
	RetryCommand          []string          `json:"retry_command"`
	RetryCommandDisplay   string            `json:"retry_command_display"`
	RetryCWD              string            `json:"retry_cwd"`
	RetryEnv              map[string]string `json:"retry_env,omitempty"`
	RequiresOriginalStdin bool              `json:"requires_original_stdin,omitempty"`
	Action                string            `json:"action"`
}

type managedTaskFailure struct {
	err    error
	handle string
	base   string
}

func (e *managedTaskFailure) Error() string { return e.err.Error() }
func (e *managedTaskFailure) Unwrap() error { return e.err }

func writeManagedStateAccess(out io.Writer, err error, stateRoot string, retry []string, cwd, handle string, stdin bool) bool {
	var pathErr *os.PathError
	if !errors.As(err, &pathErr) || !errors.Is(err, os.ErrPermission) {
		return false
	}
	rel, relErr := filepath.Rel(stateRoot, pathErr.Path)
	if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	quoted := make([]string, len(retry))
	for i, arg := range retry {
		quoted[i] = watch.Quote(arg)
	}
	r := managedStateAccess{
		State: "state_access_required", Error: err.Error(), Operation: pathErr.Op, Path: pathErr.Path,
		Task: handle, RetryCommand: retry, RetryCommandDisplay: strings.Join(quoted, " "), RetryCWD: cwd,
		RequiresOriginalStdin: stdin,
		Action:                "Doomer could not access its private task state. This filesystem error does not establish an authentication failure, missing upload consent, or a user/reviewer denial. Check the host sandbox: if it restricts this path, request normal host approval once to run retry_command with retry_env from retry_cwd with access to this state, preserving existing consent. Do not change sandbox settings or grants. If the host or user explicitly denies that request, stop only that Doomer operation and continue authorized coding and tests unless the user required review first. Report the blocked operation and path, who denied it and their actual reason when available; do not invent a denial reason. Give retry_command_display as a terminal recovery command and explain how to resume this same task. Do not retry a refusal without new authorization. Never report review completion without verified results.",
	}
	// Preserve an explicitly selected state directory in terminal recovery.
	// Never copy arbitrary environment variables or credentials into output.
	if directory := os.Getenv("DOOMER_CONFIG_DIR"); directory != "" {
		r.RetryEnv = map[string]string{"DOOMER_CONFIG_DIR": directory}
		r.RetryCommandDisplay = "env " + watch.Quote("DOOMER_CONFIG_DIR="+directory) + " " + r.RetryCommandDisplay
	}
	if handle == "" {
		r.Action += " No task handle was available at this failure; preserve any returned --base in retry_command and do not create a different baseline after editing."
	}
	if stdin {
		r.Action += " This command requires its original stdin payload; preserve it for an approved retry. Do not present the command alone as sufficient or put its payload in shell arguments."
	}
	return json.NewEncoder(out).Encode(r) == nil
}

func managedRetryCommand(c *cobra.Command, api, profile string, args []string) ([]string, string, bool) {
	binary, _ := os.Executable()
	retry := []string{binary, "--api-url", api, "--profile", profile}
	retry = append(retry, strings.Fields(c.CommandPath())[1:]...)
	retry = append(retry, args...)
	seen := map[string]bool{"api-url": true, "profile": true}
	add := func(f *pflag.Flag) {
		if !seen[f.Name] {
			retry = append(retry, "--"+f.Name+"="+f.Value.String())
			seen[f.Name] = true
		}
	}
	c.Flags().Visit(add)
	c.InheritedFlags().Visit(add)
	handle := ""
	if c.Flags().Changed("task") {
		handle, _ = c.Flags().GetString("task")
	}
	if c.Parent() != nil && c.Parent().Name() == "task" && len(args) == 1 {
		handle = args[0]
	}
	if c.Name() == "prepare" && handle == "" {
		repo, _ := c.Flags().GetString("repo")
		if absolute, err := filepath.Abs(repo); err == nil && !seen["repo"] {
			retry = append(retry, "--repo="+absolute)
		}
		prompt, _ := c.Flags().GetString("prompt")
		if prompt != "" {
			// Capture before local work continues, without initializing private state.
			ref, _ := c.Flags().GetString("base")
			if base, err := exec.CommandContext(c.Context(), "git", "-C", repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}").Output(); err == nil {
				argument := "--base=" + strings.TrimSpace(string(base))
				if seen["base"] {
					for i, arg := range retry {
						if strings.HasPrefix(arg, "--base=") {
							retry[i] = argument
						}
					}
				} else {
					retry = append(retry, argument)
				}
			}
		}
	}
	stdin := c.Parent() != nil && c.Parent().Name() == "task" && (c.Name() == "respond" || c.Name() == "context" || c.Name() == "hook")
	return retry, handle, stdin
}

func wrapManagedStateRecovery(root *cobra.Command, api, profile *string) {
	var wrap func(*cobra.Command)
	wrap = func(c *cobra.Command) {
		if c.RunE != nil && (c.Name() == "prepare" || c.Parent() != nil && c.Parent().Name() == "task") {
			run := c.RunE
			c.RunE = func(c *cobra.Command, args []string) error {
				// Resolve the original baseline before invoking code that may fail
				// after another process or hook changes HEAD.
				retry, handle, stdin := managedRetryCommand(c, *api, *profile, args)
				err := run(c, args)
				if err == nil {
					return nil
				}
				s, locationErr := managedStoreLocation()
				if locationErr == nil {
					// Claude may supply its actual session inside the handler.
					if c.Flags().Changed("session") {
						hasSession := false
						for _, arg := range retry {
							hasSession = hasSession || strings.HasPrefix(arg, "--session=")
						}
						if !hasSession {
							session, _ := c.Flags().GetString("session")
							retry = append(retry, "--session="+session)
						}
					}
					var saved *managedTaskFailure
					if errors.As(err, &saved) && saved.handle != "" {
						handle = saved.handle
						binary, _ := os.Executable()
						retry = []string{binary, "--api-url", *api, "--profile", *profile, "prepare", "--task", handle}
						if session, _ := c.Flags().GetString("session"); session != "" {
							retry = append(retry, "--session", session)
						}
						for _, flag := range []string{"allow-updates", "portable"} {
							if c.Flags().Changed(flag) {
								value, _ := c.Flags().GetBool(flag)
								retry = append(retry, fmt.Sprintf("--%s=%t", flag, value))
							}
						}
					} else if saved != nil && saved.base != "" {
						found := false
						for i, arg := range retry {
							if strings.HasPrefix(arg, "--base=") {
								retry[i] = "--base=" + saved.base
								found = true
							}
						}
						if !found {
							retry = append(retry, "--base="+saved.base)
						}
					}
					cwd, _ := os.Getwd()
					writeManagedStateAccess(c.OutOrStdout(), err, s.Root, retry, cwd, handle, stdin)
				}
				return err
			}
		}
		for _, child := range c.Commands() {
			wrap(child)
		}
	}
	wrap(root)
}

func managedDispatchStateError(out io.Writer, err error, sRoot string, args []string, handle string) error {
	binary, binaryErr := os.Executable()
	if binaryErr != nil {
		return fmt.Errorf("%w (resolve recovery executable: %v)", err, binaryErr)
	}
	cwd, _ := os.Getwd()
	stdin := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "task" && (args[i+1] == "respond" || args[i+1] == "context" || args[i+1] == "hook") {
			stdin = true
		}
	}
	writeManagedStateAccess(out, err, sRoot, append([]string{binary}, args...), cwd, handle, stdin)
	return err
}
