package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestClaudePermissionsPreserveSettingsAndOwnedReset(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, _ := claudePermissionPath()
	os.MkdirAll(filepath.Dir(path), 0700)
	original := `{"permissions":{"allow":["Bash(doomer task status *)","Read"],"deny":["Bash(curl *)"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo keep-me"}]}]},"theme":"dark"}`
	os.WriteFile(path, []byte(original), 0640)
	s, _ := managedStore()
	p := inspectClaudePermissions()
	if p.State != "missing" || len(p.Rules) != 4 {
		t.Fatal(p)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatal("inspection changed settings")
	}
	for i := 0; i < 2; i++ {
		if err := installClaudePermissions(s); err != nil {
			t.Fatal(err)
		}
	}
	data, obj, allow, err := readClaudePermissionConfig(path)
	if err != nil || len(allow) != 5 || obj["theme"] != "dark" {
		t.Fatal(string(data), err)
	}
	if inspectClaudePermissions().State != "configured" {
		t.Fatal("missing installed rules")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatal("changed settings mode")
	}
	backups, _ := filepath.Glob(path + ".before-doomer-permissions-*")
	if len(backups) != 1 {
		t.Fatal("non-idempotent backups", backups)
	}
	backup, _ := os.ReadFile(backups[0])
	if string(backup) != original {
		t.Fatal("lost backup")
	}
	// Only newly added rules belong to reset; a matching pre-existing rule stays.
	plan := &resetPlan{states: map[string]bool{}, repos: map[string]bool{}}
	plan.discoverState(filepath.Dir(s.Root))
	plan.discoverAgent("Claude", filepath.Dir(path))
	for _, entry := range plan.Entries {
		if entry.Path == path && entry.apply != nil {
			if err := entry.apply(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, obj, allow, err = readClaudePermissionConfig(path)
	if err != nil || len(allow) != 2 || !slices.Contains(allow, any("Bash(doomer task status *)")) || obj["theme"] != "dark" {
		t.Fatal(obj, err)
	}
	afterReset, _ := os.ReadFile(path)
	if !strings.Contains(string(afterReset), "echo keep-me") {
		t.Fatal("unrelated hook lost")
	}
}

func TestClaudePermissionsRejectUnsafeConfig(t *testing.T) {
	for _, content := range []string{`{"permissions":false}`, `{"permissions":{"allow":"all"}}`, `{"permissions":{"allow":[42]}}`, `not json`} {
		t.Run(content, func(t *testing.T) {
			isolate(t)
			t.Setenv("HOME", t.TempDir())
			path, _ := claudePermissionPath()
			os.MkdirAll(filepath.Dir(path), 0700)
			os.WriteFile(path, []byte(content), 0600)
			s, _ := managedStore()
			if err := installClaudePermissions(s); err == nil {
				t.Fatal("accepted invalid settings")
			}
			data, _ := os.ReadFile(path)
			if string(data) != content {
				t.Fatal("changed invalid settings")
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		isolate(t)
		t.Setenv("HOME", t.TempDir())
		path, _ := claudePermissionPath()
		os.MkdirAll(filepath.Dir(path), 0700)
		target := filepath.Join(t.TempDir(), "settings")
		os.WriteFile(target, []byte(`{}`), 0600)
		os.Symlink(target, path)
		s, _ := managedStore()
		if err := installClaudePermissions(s); err == nil {
			t.Fatal("followed symlink")
		}
	})
}

func TestClaudeOnboardingIncludesPermissionsAfterLiveConsent(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/whoami" {
			w.Write([]byte(`{"id":"account"}`))
			return
		}
		if r.URL.Path == "/v3/installations/observe" {
			w.Write([]byte(`{"accepted":true}`))
			return
		}
		t.Errorf("unexpected operation %s", r.URL.Path)
		w.WriteHeader(404)
	}))
	defer server.Close()
	store.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: "test-token"})
	s, _ := managedStore()
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: 2, Handle: handle, Agent: "claude", AccountID: "account", API: server.URL, Profile: "default", Stage: "ready"}
	s.Save(&task)
	run := func(args ...string) managedResult {
		t.Helper()
		out, err := execute(t, "", append([]string{"--api-url", server.URL, "task", "claude-permissions", handle}, args...)...)
		if err != nil {
			t.Fatal(out, err)
		}
		var r managedResult
		if err = json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatal(out, err)
		}
		return r
	}
	if r := run("--allow"); r.State != "consent_required" {
		t.Fatal(r)
	}
	path, _ := claudePermissionPath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("wrote settings without consent")
	}
	if out, err := execute(t, "", "--api-url", server.URL, "hooks", "--agent", "claude", "--task", handle); err == nil {
		t.Fatal("onboarding installed rules without upload approval", out)
	}
	recordIntegrationConsent(s, task, "test", true)
	if r := run(); r.ClaudePermissions == nil || r.ClaudePermissions.State != "missing" {
		t.Fatal(r)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("inspection was treated as acceptance")
	}
	if out, err := execute(t, "", "--api-url", server.URL, "hooks", "--agent", "claude", "--task", handle); err != nil {
		t.Fatal("approved onboarding did not configure command permissions", out, err)
	}
	if r := run(); r.ClaudePermissions == nil || r.ClaudePermissions.State != "configured" {
		t.Fatal(r)
	}
	recordIntegrationConsent(s, task, "revoked", false)
	if r := run("--allow"); r.State != "consent_required" {
		t.Fatal("revoked grant permitted settings change", r)
	}
}
