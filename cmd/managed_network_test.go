package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

func TestManagedNetworkHelpPreservesTaskAndOperation(t *testing.T) {
	for _, operation := range []string{"prepare", "submit", "status", "respond", "retry"} {
		t.Run(operation, func(t *testing.T) {
			task := managed.Task{Handle: strings.Repeat("a", 48), API: "https://doomer.example/api", Profile: "team profile"}
			err := managed.NetworkError("upload", &url.Error{URL: "https://storage.example/private?secret=token", Err: errors.New("denied")})
			var result managedResult
			addManagedNetworkHelp(&cobra.Command{Use: operation}, &result, task, err)
			if result.NetworkAccess == nil || result.NetworkAccess.RequiredHost != "storage.example" {
				t.Fatal(result)
			}
			retry := result.NetworkAccess.RetryCommand
			if !strings.Contains(retry, task.Handle) || !strings.Contains(retry, "'team profile'") || !strings.Contains(retry, task.API) {
				t.Fatal(retry)
			}
			want := "task " + operation
			if operation == "prepare" {
				want = "prepare --task"
			} else if operation == "retry" {
				want = "task submit"
			} else if operation == "respond" {
				want = "task status"
			}
			if !strings.Contains(retry, want) || strings.Contains(result.Action, "secret") || strings.Contains(result.Action, "token") {
				t.Fatal(result)
			}
		})
	}
}

func TestPrepareNetworkFailureCanResumeSameTask(t *testing.T) {
	credentials := isolate(t)
	repo := newWatchTestRepo(t)
	blocked := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blocked {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("proxy secret"))
			return
		}
		w.Write([]byte(`{"id":"account","type":"user"}`))
	}))
	defer server.Close()
	store, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: managed.Protocol, Handle: handle, Repo: repo, RepositoryID: repo, API: server.URL, Profile: "default", Release: "dev", Stage: "preparing", Agent: "claude", AccountID: "account"}
	if err := store.Save(&task); err != nil {
		t.Fatal(err)
	}
	if err := credentials.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	if _, err := managedConsent(store, task, "test-token", true); err != nil {
		t.Fatal(err)
	}
	run := func() managedResult {
		t.Helper()
		output, err := execute(t, "", "--api-url", server.URL, "prepare", "--task", handle)
		if err != nil {
			t.Fatal(output, err)
		}
		var result managedResult
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(output, err)
		}
		return result
	}
	result := run()
	if result.State != "network_unavailable" || result.Task != handle || result.NetworkAccess == nil || result.UploadApproved || strings.Contains(result.Error, "secret") {
		t.Fatal(result)
	}
	blocked = false
	result = run()
	if result.State != "ready" || result.Task != handle || !result.UploadApproved || result.NetworkAccess != nil {
		t.Fatal(result)
	}
}

func TestCodexNetworkRecoveryReusesSavedApproval(t *testing.T) {
	task := managed.Task{Handle: strings.Repeat("b", 48), Agent: "codex", Repo: "/repo with spaces", API: "https://doomer.example/api", Profile: "default"}
	err := managed.NetworkError("account check", &url.Error{URL: task.API, Err: errors.New("operation not permitted")})
	result := managedResult{PortableSetupCommand: []string{"doomer", "prepare", "--portable"}}
	addManagedNetworkHelp(&cobra.Command{Use: "prepare"}, &result, task, err)
	if result.NetworkAccess == nil || result.NetworkAccess.SettingsURL != "" || len(result.PortableSetupCommand) != 0 || strings.Contains(result.Action, "claude.ai") {
		t.Fatal("Codex received Claude network recovery", result)
	}
	for _, instruction := range []string{"preflight --agent codex --repo '/repo with spaces'", "normal tool network permissions", "Reuse existing grants", "Honor explicit denials", result.NetworkAccess.RetryCommand} {
		if !strings.Contains(result.Action, instruction) {
			t.Fatalf("missing recovery instruction %q: %s", instruction, result.Action)
		}
	}
}
