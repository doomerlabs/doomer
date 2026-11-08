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
