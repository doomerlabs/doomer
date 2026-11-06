package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestManagedPerCallAccountReviewLifecycle(t *testing.T) {
	credentials := isolate(t)
	repo := newWatchTestRepo(t)
	var submissions int
	var feedback int
	var approvals int
	var approvalPolls int
	var approvalRequest string
	lead := strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/setup/") && r.Header.Get("Authorization") != "Bearer selected-account" {
			t.Error("wrong account credential")
		}
		switch r.URL.Path {
		case "/v1/auth/whoami":
			w.Write([]byte(`{"id":"user-account","type":"user"}`))
		case "/v1/setup/grants":
			approvals++
			var input adversarylabs.ManagedSetupRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if !input.Resumable || input.Callback != "" || len(input.Repositories) != 1 {
				t.Fatal(input)
			}
			approvalRequest = input.Request
			json.NewEncoder(w).Encode(map[string]any{"url": "http://" + r.Host + "/cli/setup#request=" + input.Request, "expiresIn": 1800})
		case "/v1/setup/grants/poll":
			approvalPolls++
			var input map[string]string
			json.NewDecoder(r.Body).Decode(&input)
			if input["request"] != approvalRequest || input["codeVerifier"] == "" {
				t.Fatal(input)
			}
			if approvalPolls == 1 {
				w.Write([]byte(`{"state":"pending"}`))
			} else {
				w.Write([]byte(`{"state":"approved","token":"selected-account","account_id":"user-account"}`))
			}
		case "/v3/installations/observe":
			w.Write([]byte(`{"accepted":true}`))
		case "/v1/repositories":
			w.Write([]byte(`{"repository_id":"repo","snapshot_id":"snapshot","complete":true}`))
		case "/v1/reviews/workspace":
			submissions++
			var input adversarylabs.WorkspaceSubmission
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.FullReview == nil || *input.FullReview || input.UserRequest == nil || input.UserRequest.Prompt != "Fix the code" || input.Repository == "" || filepath.IsAbs(input.Repository) {
				t.Error("incorrect task context or review mode")
			}
			w.Write([]byte(`{"id":"managed-review","status":"running","look_closer_enabled":true,"full_review":{"enabled":false}}`))
		case "/v1/reviews/workspace/managed-review":
			json.NewEncoder(w).Encode(map[string]any{"id": "managed-review", "status": "running", "early_assessment": map[string]any{"status": "ready", "leads": []map[string]any{{"id": lead, "path": "new.txt", "line": 1, "concern": "verify"}}}})
		case "/v1/reviews/workspace/managed-review/feedback":
			feedback++
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	if err := credentials.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: "selected-account"}); err != nil {
		t.Fatal(err)
	}
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	id, err := watch.RepositoryIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: managed.Protocol, Handle: handle, Repo: repo, RepositoryID: id, API: server.URL, Profile: "default", Agent: "codex", Release: "2026.11.6.1", Base: "HEAD", Prompt: "Fix the code", Created: time.Now(), Stage: "ready"}
	if err = s.Save(&task); err != nil {
		t.Fatal(err)
	}
	run := func(input string, args ...string) managedResult {
		t.Helper()
		prefix := []string{"--api-url", server.URL, "--profile", "default"}
		output, err := execute(t, input, append(prefix, args...)...)
		if err != nil {
			t.Fatalf("%v: %s", err, output)
		}
		var result managedResult
		if err = json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatalf("%v: %s", err, output)
		}
		return result
	}
	if r := run("", "task", "submit", handle); r.State != "consent_required" {
		t.Fatal(r)
	}
	if r := run("", "task", "login", handle); r.State != "approval_required" || !r.SoftwareReady || !strings.Contains(r.URL, "/cli/setup#request=") {
		t.Fatal(r)
	}
	if r := run("", "task", "login", handle); r.State != "ready" || !r.Authenticated || !r.UploadApproved {
		t.Fatal(r)
	}
	if approvals != 1 {
		t.Fatal("approval duplicated", approvals)
	}
	if submissions != 0 {
		t.Fatal("uploaded before consent")
	}
	if r := run("", "task", "consent", handle, "--allow"); r.State != "consent_recorded" || !r.SoftwareReady || r.Release != task.Release || !r.Authenticated || !r.UploadApproved || r.SubmittedPromptRunes != len(task.Prompt) {
		t.Fatal(r)
	}
	if err = os.WriteFile(filepath.Join(repo, "new.txt"), []byte("a change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := run("", "task", "submit", handle); r.State != "review_pending" {
		t.Fatal(r)
	}
	if r := run("", "task", "status", handle); r.State != "responses_required" {
		t.Fatal(r)
	}
	input := `[{"lead_id":"` + lead + `","status":"dismissed","agrees":false,"reason":"Checked the implementation"}]`
	if r := run(input, "task", "respond", handle); r.State != "reviewed" {
		t.Fatal(r)
	}
	if r := run("", "task", "finish", handle); r.State != "complete" {
		t.Fatal(r)
	}
	if submissions != 1 || feedback != 1 {
		t.Fatal(submissions, feedback)
	}
	if r := run("", "task", "revoke", handle); r.State != "consent_revoked" || !r.Authenticated || r.UploadApproved || !r.SoftwareReady {
		t.Fatal(r)
	}
	if r := run("", "task", "status", handle); r.State != "consent_required" {
		t.Fatal(r)
	}
}

func TestManagedInstallAndMissingDiscovery(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	if out, err := execute(t, "", "install", "--repo", repo); err != nil {
		t.Fatal(out, err)
	}
	home := t.TempDir()
	bootstrap, err := managed.ResolveBootstrap(repo, home, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap.Policy.Protocol != managed.Protocol {
		t.Fatal(bootstrap)
	}
	output, err := execute(t, "", "prepare", "--repo", repo, "--agent", "claude", "--prompt", "a coding task")
	if err != nil {
		t.Fatal(err)
	}
	var result managedResult
	if err = json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "update_permission_required" || result.Task == "" {
		t.Fatal(result)
	}
	// No native skill catalog, callback, session ID, or daemon is involved.
	output, err = execute(t, "", "prepare", "--task", result.Task)
	if err != nil || !strings.Contains(output, "update_permission_required") {
		t.Fatal(output, err)
	}
}

func TestManagedConsentIsScopedToAccountRepositoryAndService(t *testing.T) {
	isolate(t)
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	task := managed.Task{AccountID: "a", RepositoryID: "repo", API: "https://doomer.ai/api", Profile: "default"}
	if _, err = managedConsent(s, task, "token", true); err != nil {
		t.Fatal(err)
	}
	if ok, err := managedConsent(s, task, "refreshed-token", false); err != nil || !ok {
		t.Fatal(ok, err)
	}
	for _, other := range []managed.Task{{AccountID: "b", RepositoryID: "repo", API: task.API, Profile: "default"}, {AccountID: "a", RepositoryID: "other", API: task.API, Profile: "default"}, {AccountID: "a", RepositoryID: "repo", API: task.API, Profile: "other"}} {
		if ok, _ := managedConsent(s, other, "token", false); ok {
			t.Fatal("grant leaked")
		}
	}
}

func TestManagedSessionCannotReplaceAnActiveTask(t *testing.T) {
	isolate(t)
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	first, _ := managed.NewHandle()
	second, _ := managed.NewHandle()
	task := managed.Task{Schema: managed.Protocol, Handle: first, Repo: "repo", API: "service", Profile: "default", Agent: "codex", Stage: "ready", Capabilities: managed.Capabilities{SessionID: "session"}}
	if err = s.Save(&task); err != nil {
		t.Fatal(err)
	}
	if err = bindManagedSession(s, task); err != nil {
		t.Fatal(err)
	}
	other := task
	other.Handle = second
	if err = s.Save(&other); err != nil {
		t.Fatal(err)
	}
	if err = bindManagedSession(s, other); err == nil {
		t.Fatal("active task binding was replaced")
	}
	task.Stage = "complete"
	if err = s.Save(&task); err != nil {
		t.Fatal(err)
	}
	if err = bindManagedSession(s, other); err != nil {
		t.Fatal(err)
	}
}

func TestManagedResultDisclosesPromptTruncation(t *testing.T) {
	result := resultFor(managed.Task{Prompt: strings.Repeat("x", 5000)})
	if !result.PromptTruncated || result.SubmittedPromptRunes != 4000 {
		t.Fatal(result)
	}
}

func TestManagedRuntimeHomeCanBeIsolated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fresh", "runtime")
	t.Setenv("DOOMER_RUNTIME_HOME", root)
	home, err := managedRuntimeHome()
	if err != nil || home != root {
		t.Fatal(home, err)
	}
	t.Setenv("DOOMER_RUNTIME_HOME", "relative")
	if _, err = managedRuntimeHome(); err == nil {
		t.Fatal("accepted relative runtime root")
	}
}

func TestManagedTaskKeepsRuntimeRootWhenEnvironmentChanges(t *testing.T) {
	root := t.TempDir()
	task := managed.Task{RuntimeHome: root}
	t.Setenv("DOOMER_RUNTIME_HOME", filepath.Join(t.TempDir(), "other"))
	got, err := taskRuntimeHome(task)
	if err != nil || got != root {
		t.Fatal(got, err)
	}
	t.Setenv("DOOMER_RUNTIME_HOME", "")
	got, err = taskRuntimeHome(task)
	if err != nil || got != root {
		t.Fatal(got, err)
	}
}

func TestPersonalBootstrapRemainsInUserHomeWithIsolatedRuntime(t *testing.T) {
	isolate(t)
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	t.Setenv("DOOMER_RUNTIME_HOME", runtimeRoot)
	repo := newWatchTestRepo(t)
	text, err := managed.BootstrapText(managed.Policy{Protocol: managed.Protocol, Channel: "stable"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(userHome, ".agents/skills/doomer-review/SKILL.md")
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := execute(t, "", "prepare", "--repo", repo, "--agent", "codex")
	if err != nil {
		t.Fatal(output, err)
	}
	var result managedResult
	if err = json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	state, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	task, err := state.Load(result.Task)
	if err != nil {
		t.Fatal(err)
	}
	if task.RuntimeHome != runtimeRoot || task.Bootstrap.Scope != "personal" || task.Bootstrap.Path != path {
		t.Fatal(task)
	}
}
