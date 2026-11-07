package cmd

import (
	"context"
	"encoding/json"
	"io"
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
	isolate(t)
	repo := newWatchTestRepo(t)
	reviewGit(t, repo, "remote", "add", "origin", "https://github.com/example/project.git")
	var submissions int
	var feedback int
	var approvals int
	var approvalPolls int
	var approvalRequest string
	portableChoice := true
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
			if !input.Resumable || !input.PortableState || input.Callback != "" || len(input.Repositories) != 1 {
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
				json.NewEncoder(w).Encode(map[string]any{"state": "approved", "token": "selected-account", "account_id": "user-account", "portable_state": portableChoice})
			}
		case "/v3/installations/observe":
			w.Write([]byte(`{"accepted":true}`))
		case "/v1/repositories":
			w.Write([]byte(`{"repository_id":"repo","snapshot_id":"snapshot","complete":true}`))
		case "/v1/reviews/workspace":
			submissions++
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if _, present := fields["full_review"]; present {
				t.Error("automatic review request must omit full_review")
			}
			var input adversarylabs.WorkspaceSubmission
			if err := json.Unmarshal(raw, &input); err != nil {
				t.Error(err)
			}
			if input.FullReview != nil || input.UserRequest == nil || input.UserRequest.Prompt != "Fix the code" || input.Repository == "" || filepath.IsAbs(input.Repository) {
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
	task.Portable = true
	task.Bootstrap = managed.Bootstrap{Path: filepath.Join(repo, ".claude/skills/doomer-review/SKILL.md"), Policy: managed.Policy{Protocol: 2, Channel: "stable"}}
	if err := watch.Write(filepath.Join(s.Root, "selections", watch.Key(task.Bootstrap.Path)+".json"), selectedRelease{Policy: task.Bootstrap.Policy, Authorized: true}); err != nil {
		t.Fatal(err)
	}
	if err := watch.Write(filepath.Join(s.Root, "update-authorizations", updateAuthorizationKey(task)+".json"), updateAuthorization{Authorized: true, ApprovedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
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
	if r := run("", "task", "submit", handle); r.State != "authentication_required" {
		t.Fatal(r)
	}
	if r := run("", "task", "login", handle); r.State != "approval_required" || !r.SoftwareReady || !strings.Contains(r.URL, "/cli/setup#request=") {
		t.Fatal(r)
	}
	if r := run("", "task", "login", handle); r.State != "ready" || !r.Authenticated || !r.UploadApproved || r.Consent == nil || r.Consent.Source != "browser_repository_approval" {
		t.Fatal(r)
	}
	portablePath, _, err := portableLocation(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readPortable(portablePath); err != nil {
		t.Fatal("browser approval did not save portable setup", err)
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
	if r := run("", "task", "status", handle); r.State != "responses_required" || r.Consent == nil || strings.Join(r.Consent.Includes, ",") != "repository_snapshots,task_context,finding_responses" || r.Consent.Account != "user-account" || r.Consent.Repository != id || r.Consent.Service != server.URL {
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
	if _, err := os.Stat(portablePath); !os.IsNotExist(err) {
		t.Fatal("revocation retained portable credentials", err)
	}
	if r := run("", "task", "status", handle); r.State != "consent_required" {
		t.Fatal(r)
	}
	// A user can approve this environment without persisting portable access.
	portableChoice = false
	task.Handle, _ = managed.NewHandle()
	if err := s.Save(&task); err != nil {
		t.Fatal(err)
	}
	if r := run("", "task", "login", task.Handle); r.State != "ready" || !r.Authenticated || !r.UploadApproved {
		t.Fatal(r)
	}
	environmentOnly, err := s.Load(task.Handle)
	if err != nil || !environmentOnly.PortableDeclined || environmentOnly.PortableApproved {
		t.Fatal("environment-only choice was not preserved", err)
	}
	if _, err := os.Stat(portablePath); !os.IsNotExist(err) {
		t.Fatal("environment-only approval persisted account access", err)
	}
	previousApprovals := approvals
	if r := run("", "task", "login", task.Handle); r.State != "ready" || approvals != previousApprovals {
		t.Fatal("environment-only approval was requested again", r)
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
	for _, text := range []string{
		"Allow Doomer to stay up to date automatically?",
		"‘Allow automatic updates (recommended)’, ‘Not now’",
		"reporting of version/task-state metadata to doomer.ai",
		"Repository content requires separate upload consent.",
		"Only after approval, resume with --allow-updates.",
	} {
		if !strings.Contains(result.Action, text) {
			t.Fatalf("update permission prompt missing %q: %s", text, result.Action)
		}
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

func TestManagedApprovalPreservesLoginOnVerificationFailure(t *testing.T) {
	credentials := isolate(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/auth/whoami" {
			t.Errorf("unexpected approval request %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	key := adversarylabs.AuthKey(server.URL, "default")
	original := adversarylabs.Auth{Token: "existing-account"}
	if err := credentials.SetAuth(key, original); err != nil {
		t.Fatal(err)
	}
	store, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: managed.Protocol, Handle: handle, API: server.URL, Profile: "default", Repo: newWatchTestRepo(t), Agent: "codex", Release: "2026.11.6.1"}
	if err = store.Save(&task); err != nil {
		t.Fatal(err)
	}
	_, err = execute(t, "", "--api-url", server.URL, "task", "login", handle)
	if err == nil || !strings.Contains(err.Error(), "account_verification") {
		t.Fatal(err)
	}
	saved, ok, err := credentials.ExactAuthE(key)
	if err != nil || !ok || saved.Token != original.Token || requests == 0 {
		t.Fatal(saved, ok, err, requests)
	}
}
