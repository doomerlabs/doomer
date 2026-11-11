package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

// The same browser approval must finish local policy setup, without a second
// chat answer or CLI --allow, and must never install/trust hooks implicitly.
func TestBrowserOnboardingWaitsThenInstallsOnlyPolicy(t *testing.T) {
	for _, decision := range []string{"approved", "cancelled", "repair", "declined"} {
		t.Run(decision, func(t *testing.T) {
			isolate(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
			var allow atomic.Bool
			var polls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/auth/whoami":
					if r.Header.Get("Authorization") == "Bearer selected-account" {
						w.Write([]byte(`{"id":"account"}`))
					} else {
						w.WriteHeader(401)
					}
				case "/v1/setup/grants":
					var request adversarylabs.ManagedSetupRequest
					json.NewDecoder(r.Body).Decode(&request)
					json.NewEncoder(w).Encode(map[string]any{"url": "http://" + r.Host + "/cli/setup#request=" + request.Request})
				case "/v1/setup/grants/poll":
					polls.Add(1)
					if !allow.Load() {
						w.Write([]byte(`{"state":"pending"}`))
					} else {
						state := decision
						if state == "repair" || state == "declined" {
							state = "approved"
						}
						json.NewEncoder(w).Encode(map[string]any{"state": state, "token": "selected-account", "account_id": "account"})
					}
				case "/v3/installations/observe", "/v2/installations/activate":
					w.Write([]byte(`{"accepted":true}`))
				default:
					t.Errorf("unexpected request during onboarding: %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			s, _ := managedStore()
			handle, _ := managed.NewHandle()
			task := managed.Task{Schema: 2, Handle: handle, Agent: "codex", API: server.URL, Profile: "default", Stage: "ready"}
			s.Save(&task)
			args := []string{"--api-url", server.URL, "task", "login", handle}
			out, err := execute(t, "", args...)
			if err != nil {
				t.Fatal(out, err)
			}
			var result managedResult
			json.Unmarshal([]byte(out), &result)
			if result.State != "approval_required" || len(result.WaitCommand) == 0 || !strings.Contains(result.Action, "remembers this same permission in Codex") {
				t.Fatal(out)
			}
			if !strings.Contains(result.DisplayMarkdown, "["+result.URL+"]("+result.URL+")") || strings.Index(out, `"display_markdown"`) > strings.Index(out, `"wait_command"`) {
				t.Fatal("approval message must contain the complete link before the wait command", out)
			}
			timedOut, err := execute(t, "", append(args, "--wait", "1ms")...)
			if err != nil {
				t.Fatal(timedOut, err)
			}
			var waiting managedResult
			if err := json.Unmarshal([]byte(timedOut), &waiting); err != nil || waiting.State != "approval_required" || len(waiting.WaitCommand) == 0 {
				t.Fatal("timeout lost resumable wait", timedOut, err)
			}
			config := filepath.Join(home, ".codex/config.toml")
			done := make(chan error, 1)
			var final string
			wantPolls := polls.Load() + 1
			go func() { var e error; final, e = execute(t, "", append(args, "--wait", "5s")...); done <- e }()
			deadline := time.Now().Add(3 * time.Second)
			for polls.Load() < wantPolls && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if polls.Load() < wantPolls {
				t.Fatal("wait did not poll")
			}
			select {
			case e := <-done:
				t.Fatal("returned before browser decision", e)
			default:
			}
			if _, err := os.Stat(config); !os.IsNotExist(err) {
				t.Fatal("installed policy before approval", err)
			}
			if decision == "repair" {
				os.MkdirAll(filepath.Dir(config), 0700)
				os.WriteFile(config, []byte("[broken"), 0600)
			}
			if decision == "declined" {
				proposal := proposedCodexReviewPolicy(managedConsentEvidence{Account: "account", Service: server.URL, Profile: "default", Agent: "codex", AllRepositories: true})
				path, _ := codexConfigPath()
				proposal.ConfigPath = path
				if err := declineCodexReviewPolicy(s, proposal); err != nil {
					t.Fatal(err)
				}
			}
			allow.Store(true)
			waitErr := <-done
			if waitErr != nil {
				t.Fatal(final, waitErr)
			}
			json.Unmarshal([]byte(final), &result)
			if decision != "cancelled" {
				if !result.UploadApproved || len(result.PolicyResumeCommand) == 0 {
					t.Fatal("missing verified approval handoff", final)
				}
				if decision != "repair" {
					if _, err := os.Stat(config); !os.IsNotExist(err) {
						t.Fatal("browser wait changed policy", err)
					}
				}
				// Ordinary resume performs the separate authorized installation.
				final, waitErr = execute(t, "", args...)
			}
			if decision == "repair" {
				if waitErr == nil {
					t.Fatal("accepted malformed config")
				}
				saved, _ := s.Load(handle)
				if !saved.OnboardingPending {
					t.Fatal("lost pending policy after installation failure")
				}
				os.WriteFile(config, nil, 0600)
				final, waitErr = execute(t, "", args...)
			}
			if waitErr != nil {
				t.Fatal(final, waitErr)
			}
			json.Unmarshal([]byte(final), &result)
			if decision == "cancelled" {
				if result.State != "approval_declined" {
					t.Fatal(final)
				}
				if _, err := os.Stat(config); !os.IsNotExist(err) {
					t.Fatal("installed policy after cancellation")
				}
				return
			}
			if decision == "declined" {
				if !result.UploadApproved || result.CodexPolicyStatus.Decision != "declined" {
					t.Fatal(final)
				}
				if _, err := os.Stat(config); !os.IsNotExist(err) {
					t.Fatal("overrode explicit policy refusal")
				}
				return
			}
			if !result.UploadApproved || result.CodexPolicyStatus == nil || result.CodexPolicyStatus.State != "configured" {
				t.Fatal(final)
			}
			if _, err := os.Stat(filepath.Join(home, ".codex/hooks.json")); !os.IsNotExist(err) {
				t.Fatal("installed hooks without separate approval")
			}
			saved, _ := s.Load(handle)
			if saved.OnboardingPending {
				t.Fatal("setup still pending")
			}
		})
	}
}

func TestClaudeApprovalBrowserDelivery(t *testing.T) {
	for _, mode := range []string{"desktop", "remote", "launcher unavailable"} {
		t.Run(mode, func(t *testing.T) {
			isolate(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("DISPLAY", "fixture")
			t.Setenv("SSH_CONNECTION", "")
			t.Setenv("SSH_CLIENT", "")
			t.Setenv("SSH_TTY", "")
			if !defaultSetupBrowser() {
				t.Skip("desktop launcher is not supported on this platform")
			}
			if mode == "remote" {
				t.Setenv("SSH_CONNECTION", "fixture")
			}
			opened := 0
			var openedURL string
			previous := openSetupBrowser
			t.Cleanup(func() { openSetupBrowser = previous })
			openSetupBrowser = func(_ context.Context, link string) error {
				opened++
				openedURL = link
				if mode == "launcher unavailable" {
					return fmt.Errorf("unavailable")
				}
				return nil
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/auth/whoami":
					w.WriteHeader(http.StatusUnauthorized)
				case "/v1/setup/grants":
					var request adversarylabs.ManagedSetupRequest
					json.NewDecoder(r.Body).Decode(&request)
					json.NewEncoder(w).Encode(map[string]any{"url": "http://" + r.Host + "/cli/setup#request=" + request.Request})
				case "/v1/setup/grants/poll":
					w.Write([]byte(`{"state":"pending"}`))
				case "/v3/installations/observe", "/v2/installations/activate":
					w.Write([]byte(`{"accepted":true}`))
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			s, _ := managedStore()
			handle, _ := managed.NewHandle()
			if err := s.Save(&managed.Task{Schema: 2, Handle: handle, Agent: "claude", API: server.URL, Profile: "default", Stage: "ready"}); err != nil {
				t.Fatal(err)
			}
			args := []string{"--api-url", server.URL, "task", "login", handle}
			out, err := execute(t, "", args...)
			var initial managedResult
			if err != nil || json.Unmarshal([]byte(out), &initial) != nil || initial.State != "approval_required" {
				t.Fatal(out, err)
			}
			if initial.BrowserOpened != (mode == "desktop") || !strings.Contains(initial.DisplayMarkdown, initial.URL) {
				t.Fatal(out)
			}
			if mode == "remote" {
				if opened != 0 {
					t.Fatal("remote session launched a local browser")
				}
				return
			}
			if openedURL != initial.URL || opened != 1 {
				t.Fatal("launcher did not receive the verified approval URL", openedURL, initial.URL, opened)
			}
			if mode == "desktop" {
				out, err = execute(t, "", append(args, "--wait", "1ms")...)
				if err != nil || opened != 1 {
					t.Fatal("repeated wait reopened the browser", out, err, opened)
				}
			}
		})
	}
}
