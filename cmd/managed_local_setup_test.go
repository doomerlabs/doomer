package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

func TestPrepareNetworkFailureOffersLocalPolicyWithoutAuthorizingUpload(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	t.Setenv("CODEX_HOME", t.TempDir())
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	id, err := watch.RepositoryIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	grant := managed.Task{AccountID: "saved-account", RepositoryID: id, Repo: repo, API: server.URL, Profile: "default", Agent: "codex"}
	if err := recordIntegrationConsent(s, grant, "explicit_integration_approval", true); err != nil {
		t.Fatal(err)
	}
	task := grant
	task.AccountID = ""
	task.Handle = "local-preview-test"
	command := &cobra.Command{Use: "prepare"}
	command.SetContext(context.Background())
	var out bytes.Buffer
	command.SetOut(&out)
	if err := outputManaged(command, s, task, managedResult{State: "network_unavailable", NetworkAccess: &managedNetworkAccess{RequiredHost: "127.0.0.1"}}); err != nil {
		t.Fatal(err)
	}
	var result managedResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.LocalSetup == nil || len(result.LocalSetup.SavedConsents) != 1 {
		t.Fatal("missing local preview", out.String())
	}
	preview := result.LocalSetup.SavedConsents[0].CodexPolicyPreview
	if preview == nil || len(preview.InstallCommand) != 0 {
		t.Fatal("local preview missing or authorized installation prematurely", out.String())
	}
	if result.Authenticated || result.UploadApproved || result.LocalSetup.AccountVerified || !result.LocalSetup.LocalOnly || requests != 0 {
		t.Fatal("local recovery contacted service or claimed authorization", out.String(), requests)
	}
	if _, err := os.Stat(os.Getenv("CODEX_HOME") + "/config.toml"); !os.IsNotExist(err) {
		t.Fatal("preview changed Codex settings", err)
	}
}
