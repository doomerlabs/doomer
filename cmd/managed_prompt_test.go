package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

func TestManagedHookCapturesPromptAndPreservesSubmittedContext(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	store := managed.Store{Root: t.TempDir()}
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: managed.Protocol, Handle: handle, Repo: repo, Stage: "ready"}
	run := func(event watch.HookInput) error {
		raw, _ := json.Marshal(event)
		command := &cobra.Command{}
		command.SetContext(context.Background())
		command.SetIn(bytes.NewReader(raw))
		return managedHook(command, store, &task, managed.Engine{}, "token")
	}
	if err := run(watch.HookInput{CWD: repo, Event: "UserPromptSubmit", Prompt: "Change the background"}); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(handle)
	if err != nil || saved.Prompt != "Change the background" {
		t.Fatal(saved.Prompt, err)
	}
	if err := run(watch.HookInput{CWD: repo, Event: "UserPromptSubmit", Prompt: "Approval reply"}); err != nil {
		t.Fatal(err)
	}
	if task.Prompt != "Change the background" {
		t.Fatal("replaced task context")
	}
	task.LastError = "review_submission failed: request failed: 400 Bad Request: invalid_user_request"
	for _, event := range []string{"PostToolUse", "Stop", "Stop"} {
		if err := run(watch.HookInput{CWD: repo, Event: event}); err == nil || !strings.Contains(err.Error(), "invalid_user_request") {
			t.Fatal("failed hook retried submission", err)
		}
	}
}

func TestManagedFailedSubmissionDoesNotReopenStopLoop(t *testing.T) {
	for _, state := range []string{"operation_failed", "review_failed"} {
		raw, _ := json.Marshal(managedResult{State: state, Error: "safe diagnostic", Review: &adversarylabs.WorkspaceReview{ID: "review"}})
		for i := 0; i < 3; i++ {
			if feedback := managedHookFeedback("Stop", raw, nil); len(feedback) != 0 {
				t.Fatal("failed Stop must be silent to avoid reopening Claude", feedback)
			}
		}
		if feedback := managedHookFeedback("PostToolUse", raw, nil); len(feedback) == 0 {
			t.Fatal("operation failure must remain visible before Stop")
		}
	}
	if feedback := managedHookFeedback("Stop", nil, errors.New("hook execution failed")); feedback["decision"] != "block" {
		t.Fatal("unverified hook output removed completion gate", feedback)
	}
}

func TestManagedFailedHookWaitsForExplicitRetry(t *testing.T) {
	credentials := isolate(t)
	repo := newWatchTestRepo(t)
	attempts := 0
	fail := true
	ready := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/whoami":
			w.Write([]byte(`{"id":"account","type":"user"}`))
		case "/v1/repositories":
			w.Write([]byte(`{"repository_id":"repo","snapshot_id":"snapshot","complete":true}`))
		case "/v1/reviews/workspace":
			attempts++
			if fail {
				w.WriteHeader(400)
				w.Write([]byte(`{"error":"invalid_user_request","message":"private response"}`))
				return
			}
			w.Write([]byte(`{"id":"review","status":"running","look_closer_enabled":true,"full_review":{"enabled":false}}`))
		case "/v1/reviews/workspace/review":
			if ready {
				w.Write([]byte(`{"id":"review","status":"completed","early_assessment":{"status":"ready","leads":[]}}`))
			} else {
				w.Write([]byte(`{"id":"review","status":"running"}`))
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	if err := credentials.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: "token"}); err != nil {
		t.Fatal(err)
	}
	store, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := managed.NewHandle()
	identity, err := watch.RepositoryIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	task := managed.Task{Schema: managed.Protocol, Handle: handle, Repo: repo, RepositoryID: identity, API: server.URL, Profile: "default", Agent: "codex", AccountID: "account", Release: "2026.11.6.1", Base: "HEAD", Stage: "ready"}
	if err = store.Save(&task); err != nil {
		t.Fatal(err)
	}
	if err = recordManagedConsent(store, task, "browser_repository_approval"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, "new.txt"), []byte("change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(operation, event string) managedResult {
		t.Helper()
		raw, _ := json.Marshal(watch.HookInput{CWD: repo, Event: event})
		output, err := execute(t, string(raw), "--api-url", server.URL, "task", operation, handle)
		if err != nil {
			t.Fatal(err)
		}
		var result managedResult
		if err = json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(output, err)
		}
		return result
	}
	first := run("hook", "PostToolUse")
	if first.State != "operation_failed" || !strings.Contains(first.Error, "invalid_user_request") {
		t.Fatal(first)
	}
	for _, event := range []string{"PreToolUse", "PostToolUse", "UserPromptSubmit", "Stop", "Stop"} {
		run("hook", event)
	}
	if attempts != 1 {
		t.Fatal("automatic retry loop", attempts)
	}
	fail = false
	if result := run("submit", ""); result.State != "review_pending" || result.Error != "" {
		t.Fatal(result)
	}
	if attempts != 2 {
		t.Fatal(attempts)
	}

	if result := run("hook", "Stop"); result.State != "review_pending" || result.Error != "" {
		t.Fatal("pending review latched failure", result)
	}
	ready = true
	if result := run("hook", "Stop"); result.State != "complete" || result.Error != "" {
		t.Fatal("ready review did not complete", result)
	}
	saved, err := store.Load(handle)
	if err != nil || saved.LastError != "" {
		t.Fatal(saved.LastError, err)
	}
	// An explicit successful status refresh clears a stale error before
	// rendering the finish action for an existing completed review.
	saved.Stage = "reviewed"
	saved.LastError = "old status request failed"
	if err := store.Save(&saved); err != nil {
		t.Fatal(err)
	}
	result := run("status", "")
	if result.State != "reviewed" || result.Error != "" || !strings.Contains(result.Action, "doomer task finish "+handle) {
		t.Fatal("status did not restore finish guidance", result)
	}
	if result := run("finish", ""); result.State != "complete" || result.Error != "" {
		t.Fatal(result)
	}
}

func TestPrepareResumesSetupAsCodingWithExplicitPrompt(t *testing.T) {
	isolate(t)
	store, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: managed.Protocol, Handle: handle, Purpose: "setup", Stage: "complete", Paused: true, API: "https://doomer.ai/api", Profile: "default"}
	if err := store.Save(&task); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "prepare", "--task", handle, "--prompt", "Fix the code"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(handle)
	if err != nil || saved.Purpose != "coding" || saved.Prompt != "Fix the code" || saved.Stage != "ready" || !saved.Paused {
		t.Fatal(saved, err)
	}
	if strings.Contains(managedTaskInstructions(saved), "This is a setup-only task") {
		t.Fatal("coding resume retained setup-only instructions")
	}
}

func TestPrepareRetainsExplicitBaselineAfterLocalWork(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	base := reviewGit(t, repo, "rev-parse", "HEAD")
	reviewGit(t, repo, "commit", "--allow-empty", "-m", "local work before preparation")
	if out, err := execute(t, "", "install", "--repo", repo); err != nil {
		t.Fatal(out, err)
	}
	out, err := execute(t, "", "prepare", "--repo", repo, "--agent", "codex", "--prompt", "Review local work", "--base", base)
	if err != nil {
		t.Fatal(out, err)
	}
	var result managedResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(out, err)
	}
	store, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.Load(result.Task)
	if err != nil || task.Base != base {
		t.Fatal(task.Base, err)
	}
	if _, err := execute(t, "", "prepare", "--task", task.Handle, "--base", "HEAD"); err == nil {
		t.Fatal("changed an existing task baseline")
	}
	if _, err := execute(t, "", "prepare", "--repo", repo, "--prompt", "Invalid base", "--base", "missing-commit"); err == nil {
		t.Fatal("accepted an invalid baseline")
	}
}

func TestPrepareBindsSessionWhenResumingSetup(t *testing.T) {
	isolate(t)
	store, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: managed.Protocol, Handle: handle, Agent: "claude", Repo: "/fixture/repo", API: "https://doomer.ai/api", Profile: "default", Paused: true, Stage: "ready"}
	if err := store.Save(&task); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "prepare", "--task", handle, "--session", "claude-session"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Load(handle)
	if err != nil || saved.Capabilities.SessionID != "claude-session" || !saved.Capabilities.Hooks {
		t.Fatal(saved, err)
	}
	key := watch.Key(task.API + "\x00" + task.Profile + "\x00" + task.Agent + "\x00" + task.Repo + "\x00claude-session")
	var bound string
	if err := watch.Read(filepath.Join(store.Root, "sessions", key+".json"), &bound); err != nil || bound != handle {
		t.Fatal(bound, err)
	}
	if _, err := execute(t, "", "prepare", "--task", handle, "--session", "replacement-session"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "sessions", key+".json")); !os.IsNotExist(err) {
		t.Fatal("old session still owns task", err)
	}
	current, err := store.Load(handle)
	if err != nil {
		t.Fatal(err)
	}
	if current.Capabilities.SessionID != "replacement-session" {
		t.Fatal(current.Capabilities)
	}
	newKey := watch.Key(task.API + "\x00" + task.Profile + "\x00" + task.Agent + "\x00" + task.Repo + "\x00replacement-session")
	newPath := filepath.Join(store.Root, "sessions", newKey+".json")
	if err := watch.Read(newPath, &bound); err != nil || bound != handle {
		t.Fatal("replacement binding missing", bound, err)
	}
	// Simulate a crash after saving the new session but before repairing its index.
	if err := os.Remove(newPath); err != nil {
		t.Fatal(err)
	}
	if err := watch.Write(filepath.Join(store.Root, "sessions", key+".json"), handle); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "", "prepare", "--task", handle, "--session", "replacement-session"); err != nil {
		t.Fatal(err)
	}
	if err := watch.Read(newPath, &bound); err != nil || bound != handle {
		t.Fatal(bound, err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "sessions", key+".json")); !os.IsNotExist(err) {
		t.Fatal("stale binding survived repair", err)
	}

}

func TestPrepareDefaultsToActiveClaudeHarness(t *testing.T) {
	isolate(t)
	t.Setenv("CLAUDECODE", "1")
	api, profile := "https://doomer.ai/api", "default"
	command := newPrepareCommand(nil, &api, &profile)
	if command.Flag("agent").DefValue != "claude" {
		t.Fatal(command.Flag("agent").DefValue)
	}
}

func TestPendingSetupRequiresAnUnambiguousScope(t *testing.T) {
	store := managed.Store{Root: t.TempDir()}
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: managed.Protocol, Handle: handle, Purpose: "setup", Stage: "ready", API: "https://doomer.ai/api", Profile: "default", Agent: "claude", Repo: "/repo"}
	if err := store.Save(&task); err != nil {
		t.Fatal(err)
	}
	got, err := pendingManagedSetup(store, task.API, task.Profile, task.Agent, task.Repo)
	if err != nil || got != handle {
		t.Fatal(got, err)
	}
	got, err = pendingManagedSetup(store, task.API, task.Profile, "codex", task.Repo)
	if err != nil || got != "" {
		t.Fatal(got, err)
	}
	task.Handle, _ = managed.NewHandle()
	if err := store.Save(&task); err != nil {
		t.Fatal(err)
	}
	got, err = pendingManagedSetup(store, task.API, task.Profile, task.Agent, task.Repo)
	if err != nil || got != "" {
		t.Fatal("ambiguous setup selected", got, err)
	}
}

func TestUnpreparedStopDoesNotRestartSetup(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	raw, _ := json.Marshal(watch.HookInput{Event: "Stop", Session: "new-session", CWD: repo})
	output, err := execute(t, string(raw), "agent-hook", "--agent", "claude", "--managed-task-adapter")
	if err != nil {
		t.Fatal(err)
	}
	var feedback map[string]any
	if err := json.Unmarshal([]byte(output), &feedback); err != nil || len(feedback) != 0 {
		t.Fatal(output, err)
	}
	raw, _ = json.Marshal(watch.HookInput{Event: "PreToolUse", Session: "new-session", CWD: repo})
	output, err = execute(t, string(raw), "agent-hook", "--agent", "claude", "--managed-task-adapter")
	if err != nil || !strings.Contains(output, "prepare --task HANDLE --session") || !strings.Contains(output, "Do not create a replacement task for recovery") {
		t.Fatal(output, err)
	}
}

func TestPrepareReusesPendingSetupWithoutAnExplicitHandle(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	store, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: managed.Protocol, Handle: handle, Purpose: "setup", Stage: "preparing", API: "https://doomer.ai/api", Profile: "default", Agent: "claude", Repo: repo}
	if err := store.Save(&task); err != nil {
		t.Fatal(err)
	}
	output, err := execute(t, "", "prepare", "--agent", "claude", "--repo", repo)
	if err != nil {
		t.Fatal(err)
	}
	var result managedResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(output, err)
	}
	files, err := filepath.Glob(filepath.Join(store.Root, "tasks", "*.json"))
	if err != nil || len(files) != 1 || result.Task != handle {
		t.Fatal(result, files, err)
	}
}

func TestReviewedTaskIncludesFinishAction(t *testing.T) {
	task := managed.Task{Handle: "task-handle", Stage: "reviewed", Review: &adversarylabs.WorkspaceReview{ID: "review"}}
	result := resultFor(task)
	if !strings.Contains(result.Action, "doomer task finish task-handle") || !strings.Contains(result.Action, "review field") {
		t.Fatal(result.Action)
	}
	task.LastError = "recorded failure"
	result = resultFor(task)
	if result.Action != "" || result.Error != task.LastError {
		t.Fatal("failed task offered completion", result)
	}
	task.LastError = ""
	task.Review = nil
	result = resultFor(task)
	if result.Action != "" {
		t.Fatal("missing review offered completion", result)
	}
}

func TestSubmittedTaskIncludesRecoveryCompletionAction(t *testing.T) {
	task := managed.Task{Handle: "existing-task", Stage: "review_pending", Review: &adversarylabs.WorkspaceReview{ID: "review", Status: "completed"}}
	result := resultFor(task)
	for _, command := range []string{"doomer task status existing-task", "doomer task finish existing-task"} {
		if !strings.Contains(result.Action, command) {
			t.Fatal(result.Action)
		}
	}
	task.LastError = "submission failed"
	if got := resultFor(task); got.Action != "" {
		t.Fatal(got.Action)
	}
}

func TestChangedCheckoutOffersSameTaskSubmission(t *testing.T) {
	task := managed.Task{Handle: "existing-task", Stage: "reviewed", LastError: managed.ErrCheckoutChanged.Error()}
	result := resultFor(task)
	for _, command := range []string{"doomer task submit existing-task", "doomer task status existing-task", "doomer task finish existing-task"} {
		if !strings.Contains(result.Action, command) {
			t.Fatal(result.Action)
		}
	}
}
