package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/paths"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/selfupdate"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// DispatchCLI only redirects the global launcher. Explicit versioned binaries
// remain useful for installation, diagnostics, and releases being tested.
var launcherExecutable = os.Executable

func DispatchCLI(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer) (bool, error) {
	if handled, err := dispatchManaged(ctx, args, in, out, stderr); handled {
		return true, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	exe, err := launcherExecutable()
	if err != nil {
		return false, err
	}
	if exe != filepath.Join(home, ".local", "bin", "doomer") {
		return false, nil
	}
	path, err := os.Getwd()
	if err != nil {
		return false, err
	}
	root := newRootCommand(nil)
	command, rest, findErr := root.Find(args)
	// Installation, protocol checks, and diagnostics must remain available
	// when the checkout pins a missing or broken release.
	if findErr == nil && (command.Name() == "prepare" || command.Name() == "task" || command.Parent() != nil && command.Parent().Name() == "task" || command.Name() == "doctor" || command.Name() == "self-update" || command.Name() == "runtime-info" || command.Name() == "supervise") {
		return false, nil
	}

	// Only interpret routing flags here. The selected release owns command and
	// flag validation, including commands and flags newer than this launcher.
	routing := pflag.NewFlagSet("launcher", pflag.ContinueOnError)
	routing.SetOutput(io.Discard)
	routing.ParseErrorsAllowlist.UnknownFlags = true
	repoPath := routing.String("repo", "", "")
	targetPath := routing.String("path", "", "")
	_ = routing.Parse(args)
	if routing.Changed("repo") {
		path = *repoPath
	}
	if routing.Changed("path") {
		path = *targetPath
	}

	// Use known command flags only as routing hints. Their validation must not
	// prevent dispatch to a newer release.
	if findErr == nil {
		command.FParseErrWhitelist.UnknownFlags = true
		if err := command.ParseFlags(rest); err == nil {
			if command.Name() == "hook" && command.Parent() != nil && command.Parent().Name() == "watch" {
				dispatch, _ := command.Flags().GetBool("dispatch")
				if dispatch {
					return false, nil
				}
			}
			if command.Name() == "watch" || command.Name() == "setup" || (command.Parent() != nil && command.Parent().Name() == "watch") {
				if paths := command.Flags().Args(); len(paths) > 0 {
					path = paths[0]
				}
			}
		}
	}

	repo, err := watching.Root(ctx, path)
	if err != nil {
		return false, nil
	}
	selected, source, err := runtimeversion.Pin(repo)
	if err != nil {
		return true, err
	}
	if selected == "" {
		return false, nil
	}
	status, err := (selfupdate.Updater{Home: home, Versioned: true}).Check(selected)
	if err != nil || status.State != "ready" {
		return true, fmt.Errorf("checkout requires Doomer %s from %s; install that verified release with self-update --side-by-side --version %s", selected, source, selected)
	}
	if err := runtimeversion.Supports(ctx, status.Path); err != nil {
		return true, err
	}
	child := exec.CommandContext(ctx, status.Path, args...)
	child.Stdin, child.Stdout, child.Stderr = in, out, stderr
	child.Env = append(runtimeversion.CleanEnvironment(os.Environ()), "DOOMER_SELECTED_PIN="+source)
	return true, child.Run()
}

func newRuntimeInfoCommand() *cobra.Command {
	return &cobra.Command{Use: "runtime-info", Hidden: true, Args: cobra.NoArgs, PersistentPreRunE: func(*cobra.Command, []string) error { return nil }, RunE: func(c *cobra.Command, _ []string) error {
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{"protocol": 1, "managed_upgrade": 1, "watch_supervisor": 1, "managed_protocol": managed.Protocol, "instruction_sha256": managedInstructionDigest()})
	}}
}

func dispatchRuntimeHooks(c *cobra.Command, api, profile, state, agent string) error {
	shared, config, err := consentPaths(api, profile)
	if err != nil || state != shared {
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{})
	}
	raw, err := io.ReadAll(io.LimitReader(c.InOrStdin(), (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return fmt.Errorf("invalid hook input")
	}
	var input watching.HookInput
	if err := json.Unmarshal(raw, &input); err != nil || input.Session == "" {
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{})
	}
	if paused(shared) {
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{})
	}
	if _, err := os.Stat(config); os.IsNotExist(err) {
		return json.NewEncoder(c.OutOrStdout()).Encode(map[string]any{})
	} else if err != nil {
		return err
	}
	var diagnostics []map[string]any
	if err := ensureWatchSupervisor(c.Context(), shared, api, profile); err != nil {
		diagnostics = append(diagnostics, map[string]any{"systemMessage": "Doomer watcher supervision is unavailable: " + err.Error()})
	}
	if input.Event == "UserPromptSubmit" {
		if err := watching.Write(filepath.Join(shared, "runtime-prompts", watching.Key(input.Session)+".json"), runtimePrompt{Input: input, At: time.Now()}); err != nil {
			return err
		}
	}
	path, pathErr := watching.HookRepository(c.Context(), input)
	var selected string
	fallback := pathErr != nil
	if pathErr == nil {
		var pinErr error
		selected, _, pinErr = runtimeversion.Pin(path)
		fallback = pinErr != nil || selected == ""
		if pinErr != nil {
			diagnostics = append(diagnostics, map[string]any{"systemMessage": "Doomer could not select a checkout runtime: " + pinErr.Error()})
		}
	}
	// Each runtime still checks its own checkout pin and upload grant. Let
	// those handlers report attribution failures when routing is uncertain.
	entries, err := os.ReadDir(filepath.Join(shared, "runtimes"))
	if os.IsNotExist(err) {
		return json.NewEncoder(c.OutOrStdout()).Encode(mergeHookOutputs(diagnostics))
	}
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(c.Context(), 28*time.Second)
	defer cancel()
	outputs := make([]map[string]any, len(entries))
	var group sync.WaitGroup
	for i, entry := range entries {
		if !entry.IsDir() || !runtimeversion.Release.MatchString(entry.Name()) {
			continue
		}
		runtimeRoot := filepath.Join(shared, "runtimes", entry.Name())
		if !fallback && !hookRuntimeSelected(runtimeRoot, entry.Name(), selected, input) {
			continue
		}
		if err := prepareHookRuntime(ctx, runtimeRoot, input, entry.Name() == selected); err != nil {
			outputs[i] = map[string]any{"systemMessage": "Doomer watcher lifecycle: " + err.Error()}
			continue
		}
		status, e := (selfupdate.Updater{Home: home, Versioned: true}).Check(entry.Name())
		if e != nil || status.State != "ready" {
			outputs[i] = map[string]any{"systemMessage": "Doomer " + entry.Name() + " could not be verified. Rerun this checkout's setup."}
			continue
		}
		group.Add(1)
		go func(i int, release string, binary string) {
			defer group.Done()
			root := filepath.Join(shared, "runtimes", release)
			var err error
			outputs[i], err = runtimeHookOutput(ctx, binary, api, profile, root, shared, agent, input, raw)
			if err != nil {
				outputs[i] = map[string]any{"systemMessage": "Doomer " + release + " hook failed. Check its runtime log."}
			}
		}(i, entry.Name(), status.Path)
	}
	group.Wait()
	return json.NewEncoder(c.OutOrStdout()).Encode(mergeHookOutputs(append(outputs, diagnostics...)))
}

// Merge each runtime's context and blocking decisions without letting an empty
// response from another version erase them. Input order is deterministic.
func mergeHookOutputs(outputs []map[string]any) map[string]any {
	result := map[string]any{}
	for _, output := range outputs {
		for _, key := range []string{"systemMessage", "reason", "stopReason"} {
			if value, ok := output[key].(string); ok && value != "" {
				if previous, ok := result[key].(string); ok && previous != "" {
					value = previous + "\n\n" + value
				}
				result[key] = value
			}
		}
		if output["decision"] == "block" {
			result["decision"] = "block"
		}
		if output["continue"] == false {
			result["continue"] = false
		}
		if specific, ok := output["hookSpecificOutput"].(map[string]any); ok {
			merged, _ := result["hookSpecificOutput"].(map[string]any)
			if merged == nil {
				merged = map[string]any{}
				result["hookSpecificOutput"] = merged
			}
			for key, value := range specific {
				if key == "additionalContext" {
					if previous, ok := merged[key].(string); ok && previous != "" {
						value = previous + "\n\n" + fmt.Sprint(value)
					}
				}
				merged[key] = value
			}
		}
	}
	return result
}

// Recover a stopped runtime on the next approved edit without changing consent.
func ensureRuntimeWatch(ctx context.Context, root, path, api, profile string) error {
	lock, err := lifecycleLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	if paused(root) {
		return nil
	}
	// Register before testing liveness so an idle watcher cannot retire between
	// this check and the hook recording its first request.
	if err := watching.RegisterRepository(root, path, "agent", false, time.Now()); err != nil {
		return err
	}
	_, running, err := runningWatch(root)
	if err != nil || running {
		return err
	}
	return startManagedWatch(ctx, root, path, api, profile, nil)
}

// Switching the global executable also affects old hooks that referenced it.
// Refuse that migration until every recorded checkout can use the dispatcher.
func validateLauncherMigration(home string) error {
	dir := os.Getenv("DOOMER_CONFIG_DIR")
	if dir == "" {
		var err error
		dir, err = paths.ConfigDir()
		if err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "watch"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var cfg watching.Config
		err := watching.Read(filepath.Join(dir, "watch", entry.Name(), "config.json"), &cfg)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := compatibleRepositoryPins(home, cfg); err != nil {
			return err
		}
	}
	return nil
}

func compatibleRepositoryPins(home string, cfg watching.Config) error {
	if cfg.AllRepositories {
		return fmt.Errorf("migrate legacy all-repository approval to explicit repositories before installing the versioned launcher")
	}
	for _, repo := range cfg.Directories {
		pin, _, err := runtimeversion.Pin(repo)
		if err != nil {
			return err
		}
		if pin == "" {
			return fmt.Errorf("%s has legacy setup without a CLI pin; update its skill before migrating the shared launcher", repo)
		}
		status, err := (selfupdate.Updater{Home: home, Versioned: true}).Check(pin)
		if err != nil || status.State != "ready" {
			return fmt.Errorf("install the pinned release %s for %s before migrating the shared launcher", pin, repo)
		}
		if err := runtimeversion.Supports(context.Background(), status.Path); err != nil {
			return err
		}
	}
	return nil
}

// A checkout that changed pins no longer keeps its previous runtime alive.
func activeRuntimeRepositories(root string, cfg watching.Config, now time.Time) (bool, error) {
	repos, err := watching.Repositories(root, cfg)
	if err != nil {
		return false, err
	}
	for _, repo := range repos {
		if repo.Active(now) && watching.Allowed(cfg, repo.Path) {
			return true, nil
		}
	}
	return false, nil
}
