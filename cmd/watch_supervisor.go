package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
	"github.com/doomerlabs/doomer/internal/runtimeversion"
	"github.com/doomerlabs/doomer/internal/version"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

const watcherIdleTimeout = 2 * time.Minute

// Automatic suspension uses the pause format understood by older releases.
// Its separate receipt prevents recovery from clearing a user's pause.
func idleRuntime(root string) bool {
	pause, err := os.ReadFile(filepath.Join(root, "paused.json"))
	if err != nil {
		return false
	}
	receipt, err := os.ReadFile(filepath.Join(root, "idle-pause.json"))
	return err == nil && bytes.Equal(pause, receipt)
}

func userPaused(root string) bool {
	shared := sharedWatchRoot(root)
	if _, err := os.Stat(filepath.Join(shared, "paused.json")); !os.IsNotExist(err) {
		return true
	}
	return paused(root) && !idleRuntime(root)
}

// Caller holds the runtime lifecycle lock, also used by explicit start/stop.
func resumeIdleRuntime(ctx context.Context, root string) error {
	if !idleRuntime(root) || userPaused(root) {
		return nil
	}
	// Keep the stop request in place until the previous worker releases its
	// lifetime lock. Reusing a worker already exiting would lose this edit.
	if err := waitWatch(ctx, func() (bool, error) {
		_, running, err := runningWatch(root)
		return !running, err
	}); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(root, "paused.json")); err != nil {
		return err
	}
	return os.Remove(filepath.Join(root, "idle-pause.json"))
}

func suspendIdleRuntime(ctx context.Context, root string) error {
	lock, err := publock.TryAcquire(root, "watch-lifecycle")
	if publock.Busy(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	if paused(root) {
		var stoppedAt time.Time
		if idleRuntime(root) && watching.Read(filepath.Join(root, "idle-pause.json"), &stoppedAt) == nil && time.Since(stoppedAt) > 15*time.Second {
			_, running, err := runningWatch(root)
			if err != nil {
				return err
			}
			if running {
				return fmt.Errorf("watcher did not stop after automatic suspension")
			}
		}
		return nil
	}
	process, running, err := runningWatch(root)
	if process.ManagedSession == "" && (err != nil || running) {
		return err
	}
	// Recheck under the same lock as hook renewal, so an edit cannot lose
	// its newly renewed lease to a supervisor that observed stale state.
	if runtimeHasWork(root, time.Now()) {
		return nil
	}
	stamp := time.Now()
	if err := watching.Write(filepath.Join(root, "idle-pause.json"), stamp); err != nil {
		return err
	}
	if err := watching.Write(filepath.Join(root, "paused.json"), stamp); err != nil {
		return err
	}
	return nil
}

func hookRuntimeSelected(root, release, selected string, input watching.HookInput) bool {
	// Prompts can precede checkout selection. Keep their context available
	// locally, without starting any watcher or capturing source.
	if input.Event == "UserPromptSubmit" || release == selected {
		return true
	}
	var turn watching.Turn
	return watching.Read(watching.TurnPath(root, input.Session), &turn) == nil &&
		turn.Owner.Session == input.Session && (input.Turn == "" || input.Turn == turn.Owner.Turn) && len(turn.Repositories) > 0
}

func prepareHookRuntime(ctx context.Context, root string, input watching.HookInput, selected bool) error {
	lock, err := lifecycleLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	if userPaused(root) {
		return nil
	}
	if selected && watching.HookStartsWork(input) || completedReviewWaiting(root, input) {
		if err := watching.Write(filepath.Join(root, "activity.json"), time.Now()); err != nil {
			return err
		}
		return resumeIdleRuntime(ctx, root)
	}
	_, running, err := runningWatch(root)
	if err != nil || running {
		return err
	}
	if runtimeHasWork(root, time.Now()) {
		return resumeIdleRuntime(ctx, root)
	}
	if !paused(root) {
		stamp := time.Now()
		if err := watching.Write(filepath.Join(root, "idle-pause.json"), stamp); err != nil {
			return err
		}
		return watching.Write(filepath.Join(root, "paused.json"), stamp)
	}
	return nil
}

func completedReviewWaiting(root string, input watching.HookInput) bool {
	if input.Event != "Stop" && input.Event != "PostToolUse" {
		return false
	}
	var turn watching.Turn
	if watching.Read(watching.TurnPath(root, input.Session), &turn) != nil || turn.Owner.Session != input.Session || input.Turn != "" && input.Turn != turn.Owner.Turn {
		return false
	}
	for _, path := range turn.Repositories {
		var feed watching.Feed
		if watching.Read(watching.FeedPath(root, path), &feed) != nil || feed.Owner != turn.Owner || feed.AssessmentStatus != "ready" || feed.ReviewID == "" || feed.Updated.IsZero() || time.Since(feed.Updated) > 24*time.Hour {
			continue
		}
		delivered := false
		for _, id := range turn.Delivered {
			delivered = delivered || id == feed.ReviewID
		}
		if !delivered {
			return true
		}
	}
	return false
}

// Inspect small review metadata only. Never scan the working tree to decide
// whether a background process is needed. Stale operations are bounded too.
func runtimeHasWork(root string, now time.Time) bool {
	var activity time.Time
	if watching.Read(filepath.Join(root, "activity.json"), &activity) == nil && now.Sub(activity) < watcherIdleTimeout {
		return true
	}
	entries, err := os.ReadDir(filepath.Join(root, "repositories"))
	if err != nil && !os.IsNotExist(err) {
		return true
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var state watching.State
		if watching.Read(filepath.Join(root, "repositories", entry.Name()), &state) != nil {
			return true
		}
		for _, operation := range state.Active {
			if operation.Attributable && !operation.Started.IsZero() && now.Sub(operation.Started) < 30*time.Minute {
				return true
			}
		}
		if state.RequestedAt.IsZero() || now.Sub(state.RequestedAt) >= 30*time.Minute {
			continue
		}
		var feed watching.Feed
		if watching.Read(filepath.Join(root, "reviews", entry.Name()), &feed) != nil || feed.Owner != state.Owner || feed.Fingerprint != state.Fingerprint || (feed.AssessmentStatus != "ready" && feed.AssessmentStatus != "unavailable") {
			return true
		}
	}
	return false
}

func ensureWatchSupervisor(ctx context.Context, shared, api, profile string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return ensureWatchSupervisorBinary(ctx, shared, api, profile, exe, version.Version)
}

func ensureWatchSupervisorBinary(ctx context.Context, shared, api, profile, exe, release string) error {
	if paused(shared) {
		return nil
	}
	root := filepath.Join(shared, "supervisor")
	lock, err := lifecycleLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	process, running, err := runningWatch(root)
	if err != nil {
		return err
	}
	if running && process.Session == release {
		return nil
	}
	if running {
		if err := watching.Write(filepath.Join(root, "paused.json"), time.Now()); err != nil {
			return err
		}
		if err := waitWatch(ctx, func() (bool, error) { _, live, err := runningWatch(root); return !live, err }); err != nil {
			return err
		}
	}
	if err := os.Remove(filepath.Join(root, "paused.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	session := rand.Text()
	child := exec.Command(exe, "--api-url", api, "--profile", profile, "watch", "supervise", "--managed-session", session)
	// The explicit config directory is inherited, including test installations.
	if err := startWatchProcess(child, filepath.Join(root, "watch.log")); err != nil {
		return err
	}
	return waitWatch(ctx, func() (bool, error) {
		process, live, err := runningWatch(root)
		return live && process.ManagedSession == session, err
	})
}

func newWatchSupervisorCommand(api, profile *string) *cobra.Command {
	var session string
	c := &cobra.Command{Use: "supervise", Hidden: true, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if session == "" {
			return fmt.Errorf("supervisor requires managed startup")
		}
		shared, _, err := consentPaths(*api, *profile)
		if err != nil {
			return err
		}
		root := filepath.Join(shared, "supervisor")
		lock, err := publock.TryAcquire(root, "watcher")
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := watching.Write(filepath.Join(root, "process.json"), watchProcess{PID: os.Getpid(), Session: version.Version, ManagedSession: session}); err != nil {
			return err
		}
		defer os.Remove(filepath.Join(root, "process.json"))
		defer os.Remove(filepath.Join(root, "heartbeat.json"))
		firstSeen := map[string]time.Time{}
		nextReconcile := time.Time{}
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if paused(shared) {
				return nil
			}
			if _, err := os.Stat(filepath.Join(shared, "config.json")); os.IsNotExist(err) {
				return nil
			} else if err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(root, "paused.json")); !os.IsNotExist(err) {
				return nil
			}
			if err := watching.Write(filepath.Join(root, "heartbeat.json"), time.Now()); err != nil {
				return err
			}
			if time.Now().After(nextReconcile) {
				if err := reconcileIdleRuntimes(c.Context(), shared, firstSeen, time.Now()); err != nil {
					fmt.Fprintln(c.ErrOrStderr(), err)
				}
				nextReconcile = time.Now().Add(5 * time.Second)
			}
			select {
			case <-c.Context().Done():
				return c.Context().Err()
			case <-ticker.C:
			}
		}
	}}
	c.Flags().StringVar(&session, "managed-session", "", "internal background startup identity")
	return c
}

func reconcileIdleRuntimes(ctx context.Context, shared string, firstSeen map[string]time.Time, now time.Time) error {
	entries, err := os.ReadDir(filepath.Join(shared, "runtimes"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if !entry.IsDir() || !runtimeversion.Release.MatchString(entry.Name()) {
			continue
		}
		root := filepath.Join(shared, "runtimes", entry.Name())
		if firstSeen[root].IsZero() {
			firstSeen[root] = now
		}
		if now.Sub(firstSeen[root]) < watcherIdleTimeout || !paused(root) && runtimeHasWork(root, now) {
			continue
		}
		if err := suspendIdleRuntime(ctx, root); err != nil {
			failures = append(failures, fmt.Errorf("retire runtime %s: %w", entry.Name(), err))
		}
	}
	return errors.Join(failures...)
}

// Retain prompts independently of worker lifetime. A sleeping legacy worker
// receives the current prompt exactly once before it handles the next edit.
type runtimePrompt struct {
	Input watching.HookInput
	At    time.Time
}

func runtimeHookOutput(ctx context.Context, binary, api, profile, root, shared, agent string, input watching.HookInput, raw []byte) (map[string]any, error) {
	if paused(root) {
		return map[string]any{}, nil
	}
	lock, err := lifecycleLock(ctx, filepath.Join(root, "hook-sessions", watching.Key(input.Session)))
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	invoke := func(data []byte) (map[string]any, error) {
		child := exec.CommandContext(ctx, binary, "--api-url", api, "--profile", profile, "watch", "hook", "--state", root, "--agent", agent)
		child.Stdin = bytes.NewReader(data)
		data, err := child.Output()
		if err != nil {
			return nil, err
		}
		var output map[string]any
		err = json.Unmarshal(data, &output)
		return output, err
	}
	var prompt runtimePrompt
	var delivered time.Time
	receipt := filepath.Join(root, "prompt-receipts", watching.Key(input.Session)+".json")
	_ = watching.Read(receipt, &delivered)
	var replay map[string]any
	if input.Event != "UserPromptSubmit" && watching.Read(filepath.Join(shared, "runtime-prompts", watching.Key(input.Session)+".json"), &prompt) == nil && prompt.At.After(delivered) && time.Since(prompt.At) < 24*time.Hour && (input.Turn == "" || prompt.Input.Turn == "" || input.Turn == prompt.Input.Turn) {
		data, err := json.Marshal(prompt.Input)
		if err != nil {
			return nil, err
		}
		replay, err = invoke(data)
		if err != nil {
			return nil, err
		}
		if err := watching.Write(receipt, prompt.At); err != nil {
			return nil, err
		}
	}
	output, err := invoke(raw)
	if err != nil {
		return nil, err
	}
	if input.Event == "UserPromptSubmit" && watching.Read(filepath.Join(shared, "runtime-prompts", watching.Key(input.Session)+".json"), &prompt) == nil {
		if err := watching.Write(receipt, prompt.At); err != nil {
			return nil, err
		}
	}
	return mergeHookOutputs([]map[string]any{replay, output}), nil
}
