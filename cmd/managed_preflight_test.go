package cmd

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func preflightTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data := []byte{}
		if !d.IsDir() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		result[path] = info.Mode().String() + string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPreflightIsLocalAndReadOnlyAcrossFreshInvocations(t *testing.T) {
	credentials := isolate(t)
	repo := newWatchTestRepo(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	const privateToken = "preflight-test-private-credential"
	if err := credentials.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: privateToken}); err != nil {
		t.Fatal(err)
	}
	id, err := watch.RepositoryIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	task := managed.Task{AccountID: "account", RepositoryID: id, Repo: repo, API: server.URL, Profile: "default", Agent: "codex"}
	if err := recordIntegrationConsent(s, task, "explicit_integration_approval", true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	if err := installCodexReviewPolicy(managedConsentEvidence{Account: task.AccountID, Service: task.API, Profile: task.Profile, Agent: task.Agent, AllRepositories: true}); err != nil {
		t.Fatal(err)
	}
	before := preflightTree(t, filepath.Dir(s.Root))
	configBefore := preflightTree(t, os.Getenv("CODEX_HOME"))
	for i := 0; i < 2; i++ {
		out, err := execute(t, "", "--api-url", server.URL, "preflight", "--repo", repo, "--agent", "codex")
		if err != nil {
			t.Fatal(err)
		}
		var result preflightResult
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(out, err)
		}
		if !result.LocalOnly || result.AccountVerified || result.State != "saved_consent_found" || len(result.SavedConsents) != 1 {
			t.Fatal(out)
		}
		status := result.SavedConsents[0].CodexPolicyStatus
		if status.State != "configured" || status.Activation != "unverified" || !strings.Contains(status.Action, "fresh session") {
			t.Fatal(status)
		}
		if strings.Contains(out, privateToken) || strings.Contains(out, `"upload_approved":true`) || strings.Contains(out, `"authenticated":true`) {
			t.Fatal(out)
		}
	}
	if requests != 0 {
		t.Fatal("preflight made network calls", requests)
	}
	if !reflect.DeepEqual(before, preflightTree(t, filepath.Dir(s.Root))) || !reflect.DeepEqual(configBefore, preflightTree(t, os.Getenv("CODEX_HOME"))) {
		t.Fatal("preflight changed local state")
	}
}

func TestPreflightMissingStateDoesNotInitialize(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	s, _ := managedStoreLocation()
	out, err := execute(t, "", "preflight", "--repo", repo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"state":"no_saved_consent"`) {
		t.Fatal(out)
	}
	if _, err := os.Stat(s.Root); !os.IsNotExist(err) {
		t.Fatal("created managed store", err)
	}
}

func TestPreflightRetainsConsentBoundariesAndRevocation(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	s, _ := managedStore()
	id, _ := watch.RepositoryIdentity(context.Background(), repo)
	task := managed.Task{AccountID: "account", RepositoryID: id, Repo: repo, API: "https://doomer.ai/api", Profile: "default", Agent: "codex"}
	if err := recordIntegrationConsent(s, task, "approved", true); err != nil {
		t.Fatal(err)
	}
	check := func(args ...string) preflightResult {
		t.Helper()
		out, err := execute(t, "", append([]string{"preflight", "--repo", repo}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		var r preflightResult
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(out, err)
		}
		return r
	}
	if r := check("--agent", "claude"); len(r.SavedConsents) != 0 {
		t.Fatal("wrong agent", r)
	}
	if r := check("--profile", "other"); len(r.SavedConsents) != 0 {
		t.Fatal("wrong profile", r)
	}
	if r := check("--api-url", "https://other.example/api"); len(r.SavedConsents) != 0 {
		t.Fatal("wrong service", r)
	}
	// A repository grant cannot leak into another repository, but covers linked worktrees.
	if err := recordIntegrationConsent(s, task, "revoked", false); err != nil {
		t.Fatal(err)
	}
	if r := check(); len(r.SavedConsents) != 0 {
		t.Fatal("revoked grant", r)
	}
	if err := recordManagedConsent(s, task, "repository approval"); err != nil {
		t.Fatal(err)
	}
	other := newWatchTestRepo(t)
	out, err := execute(t, "", "preflight", "--repo", other)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"state":"no_saved_consent"`) {
		t.Fatal(out)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	reviewGit(t, repo, "worktree", "add", "-b", "preflight-linked", linked)
	out, err = execute(t, "", "preflight", "--repo", linked)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"state":"saved_consent_found"`) {
		t.Fatal(out)
	}
	if err := watch.Write(filepath.Join(s.Root, "reset.json"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if r := check(); len(r.SavedConsents) != 0 {
		t.Fatal("reset consent", r)
	}
}

func TestPreflightMissingCodexPolicyRespectsDecline(t *testing.T) {
	isolate(t)
	t.Setenv("CODEX_HOME", t.TempDir())
	repo := newWatchTestRepo(t)
	id, err := watch.RepositoryIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := managedStore()
	task := managed.Task{AccountID: "account", RepositoryID: id, Repo: repo, API: "https://doomer.ai/api", Profile: "default", Agent: "codex"}
	if err := recordIntegrationConsent(s, task, "explicit_integration_approval", true); err != nil {
		t.Fatal(err)
	}
	for _, decline := range []bool{false, true} {
		if decline {
			evidence, err := savedManagedConsentEvidence(s, task)
			if err != nil {
				t.Fatal(err)
			}
			proposal, _, err := inspectCodexReviewPolicy(*evidence)
			if err != nil {
				t.Fatal(err)
			}
			if err = declineCodexReviewPolicy(s, proposal); err != nil {
				t.Fatal(err)
			}
		}
		out, err := execute(t, "", "preflight", "--repo", repo, "--agent", "codex")
		if err != nil {
			t.Fatal(err)
		}
		var r preflightResult
		if err = json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(err)
		}
		status := r.SavedConsents[0].CodexPolicyStatus
		if decline {
			if status.Decision != "declined" || strings.Contains(r.Action, "Before prepare, show") {
				t.Fatal(out)
			}
		} else {
			preview := r.SavedConsents[0].CodexPolicyPreview
			if status.Decision != "pending" || !strings.Contains(r.Action, "Before prepare, show") || preview == nil || !strings.Contains(preview.ExtraPolicy, `"all_repositories":true`) || len(preview.InstallCommand) != 0 {
				t.Fatal(out)
			}
		}
	}
}

func TestPreflightClaudeMissingPermissionsWithoutConsent(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	out, err := execute(t, "", "preflight", "--agent", "claude", "--repo", newWatchTestRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	var r preflightResult
	if err = json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if r.ClaudePermissions == nil || r.ClaudePermissions.State != "missing" || !strings.Contains(r.Action, "before prepare") {
		t.Fatal(out)
	}
	if r.ClaudePermissions.InstallCommand != nil {
		t.Fatal("unverified installation offered", out)
	}
}

func TestPreflightRecordsLocalPolicyDeclineWithoutTaskOrNetwork(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			isolate(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			repo := newWatchTestRepo(t)
			id, err := watch.RepositoryIdentity(context.Background(), repo)
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
			defer server.Close()
			s, _ := managedStore()
			task := managed.Task{AccountID: "account", RepositoryID: id, Repo: repo, API: server.URL, Profile: "default", Agent: agent}
			if err := recordIntegrationConsent(s, task, "explicit_integration_approval", true); err != nil {
				t.Fatal(err)
			}
			out, err := execute(t, "", "--api-url", server.URL, "preflight", "--repo", repo, "--agent", agent)
			if err != nil {
				t.Fatal(err)
			}
			var first preflightResult
			if err = json.Unmarshal([]byte(out), &first); err != nil {
				t.Fatal(err)
			}
			var command []string
			if agent == "codex" {
				command = first.SavedConsents[0].CodexPolicyPreview.DeclineCommand
			} else {
				command = first.SavedConsents[0].ClaudePermissions.DeclineCommand
			}
			if len(command) == 0 {
				t.Fatal("missing handle-free decline command", out)
			}
			for i := 0; i < 2; i++ {
				out, err = execute(t, "", command[1:]...)
				if err != nil {
					t.Fatal(err)
				}
				var result preflightResult
				if err = json.Unmarshal([]byte(out), &result); err != nil || result.State != "reviewer_policy_declined" {
					t.Fatal(out, err)
				}
			}
			out, err = execute(t, "", "--api-url", server.URL, "preflight", "--repo", repo, "--agent", agent)
			if err != nil {
				t.Fatal(err)
			}
			var after preflightResult
			if err = json.Unmarshal([]byte(out), &after); err != nil {
				t.Fatal(err)
			}
			if agent == "codex" {
				if after.SavedConsents[0].CodexPolicyStatus.Decision != "declined" || after.SavedConsents[0].CodexPolicyPreview != nil {
					t.Fatal(out)
				}
			} else if p := after.SavedConsents[0].ClaudePermissions; p.Decision != "declined" || p.InstallCommand != nil || p.DeclineCommand != nil {
				t.Fatal(out)
			}
			if requests != 0 {
				t.Fatal("decline contacted service", requests)
			}
			if tasks, _ := filepath.Glob(filepath.Join(s.Root, "tasks", "*.json")); len(tasks) != 0 {
				t.Fatal("decline created task", tasks)
			}
			var config string
			if agent == "codex" {
				config, _ = codexConfigPath()
			} else {
				config, _ = claudePermissionPath()
			}
			if _, err := os.Stat(config); !os.IsNotExist(err) {
				t.Fatal("decline changed settings", err)
			}
			evidence, err := savedManagedConsentEvidence(s, task)
			if err != nil || evidence == nil {
				t.Fatal("decline lost upload grant", err)
			}
			if agent == "codex" {
				if err := installCodexReviewPolicy(*evidence); err != nil {
					t.Fatal(err)
				}
				before, _ := os.ReadFile(config)
				if _, err := execute(t, "", command[1:]...); err != nil {
					t.Fatal(err)
				}
				proposal, _, err := inspectCodexReviewPolicy(*evidence)
				if err != nil {
					t.Fatal(err)
				}
				declined, err := codexReviewPolicyDeclined(s, proposal)
				after, _ := os.ReadFile(config)
				if err != nil || !declined || string(before) != string(after) {
					t.Fatal("configured decline was not persisted without changing settings", err)
				}
			}
			if _, err := execute(t, "", "--api-url", server.URL, "preflight", "--repo", repo, "--agent", agent, "--decline-policy", "unknown-account"); err == nil {
				t.Fatal("accepted unknown scope")
			}
		})
	}
}

func TestPreflightReportsDisabledClaudeHooksWithoutWritesOrNetwork(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	repo := newWatchTestRepo(t)
	id, err := watch.RepositoryIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) }))
	defer server.Close()
	s, _ := managedStore()
	task := managed.Task{AccountID: "account", RepositoryID: id, Repo: repo, API: server.URL, Profile: "default", Agent: "claude"}
	if err := recordIntegrationConsent(s, task, "explicit_integration_approval", true); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	path := watch.HookFile(home, "claude")
	if err := watch.Install(path, "doomer agent-hook --managed-task-adapter", "--managed-task-adapter", false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	config["disableAllHooks"] = true
	data, _ = json.Marshal(config)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	before := preflightTree(t, home)
	for i := 0; i < 2; i++ {
		out, err := execute(t, "", "--api-url", server.URL, "preflight", "--repo", repo, "--agent", "claude")
		if err != nil {
			t.Fatal(err)
		}
		var result preflightResult
		if err = json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		hooks := result.SavedConsents[0].HookSetup
		if hooks == nil || hooks.State != "configured" || hooks.Activation != "disabled_by_host" || !strings.Contains(hooks.Action, "/hooks") || hooks.InstallCommand != nil || hooks.DeclineCommand != nil {
			t.Fatal(out)
		}
	}
	if requests != 0 || !reflect.DeepEqual(before, preflightTree(t, home)) {
		t.Fatal("local hook inspection wrote state or contacted the service")
	}
}
