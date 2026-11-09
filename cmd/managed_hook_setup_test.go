package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestManagedHookSetupDecisionsAndTrustRemainSeparate(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			store := isolate(t)
			t.Setenv("HOME", t.TempDir())
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/auth/whoami" {
					w.Write([]byte(`{"id":"account"}`))
					return
				}
				if r.URL.Path == "/v1/repositories" || r.URL.Path == "/v1/reviews/workspace" {
					t.Error("setup uploaded source")
				}
				w.Write([]byte(`{"accepted":true}`))
			}))
			defer server.Close()
			store.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: "test-token"})
			s, _ := managedStore()
			handle, _ := managed.NewHandle()
			task := managed.Task{Schema: 2, Handle: handle, Agent: agent, AccountID: "account", API: server.URL, Profile: "default", Stage: "ready"}
			s.Save(&task)
			run := func(flags ...string) managedResult {
				t.Helper()
				out, err := execute(t, "", append([]string{"--api-url", server.URL, "task", "hook-setup", handle}, flags...)...)
				if err != nil {
					t.Fatal(out, err)
				}
				var r managedResult
				if err = json.Unmarshal([]byte(out), &r); err != nil {
					t.Fatal(err)
				}
				return r
			}
			if r := run("--allow"); r.State != "consent_required" {
				t.Fatal(r)
			}
			recordIntegrationConsent(s, task, "test", true)
			if r := run(); r.HookSetup == nil || r.HookSetup.State != "missing" || len(r.HookSetup.InstallCommand) == 0 {
				t.Fatal(r)
			}
			if r := run("--decline"); r.HookSetup.Decision != "declined" || !r.UploadApproved {
				t.Fatal(r)
			}
			if r := run(); r.HookSetup.Decision != "declined" || len(r.HookSetup.InstallCommand) != 0 {
				t.Fatal(r)
			}
			if r := run("--allow"); r.HookSetup.State != "configured" || r.HookSetup.Activation != "unverified" {
				t.Fatal(r)
			}
			if agent == "claude" && inspectClaudePermissions().State != "configured" {
				t.Fatal("Claude rules not installed with hooks")
			}
			// Deleting the file requires setup again, but an old callback must not
			// claim that the missing hooks are currently active.
			r := run()
			if err := os.Remove(r.HookSetup.ConfigPath); err != nil {
				t.Fatal(err)
			}
			if r := run(); r.HookSetup.State != "missing" || r.HookSetup.Decision != "pending" {
				t.Fatal(r)
			}
		})
	}
}
