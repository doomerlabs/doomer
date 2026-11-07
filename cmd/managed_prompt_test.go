package cmd

import (
	"bytes"
	"context"
	"encoding/json"
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
	raw, _ := json.Marshal(managedResult{State: "operation_failed", Error: "safe diagnostic"})
	for i := 0; i < 3; i++ {
		feedback := managedHookFeedback("Stop", raw, nil)
		if feedback["decision"] == "block" {
			t.Fatal("failed submission reopened chat", feedback)
		}
		body, _ := json.Marshal(feedback)
		if !strings.Contains(string(body), "incomplete") {
			t.Fatal("failure presented as success", feedback)
		}
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
