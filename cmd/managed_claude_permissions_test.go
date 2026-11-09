package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
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

func TestClaudeExplicitDenyIsNotMissingPermission(t *testing.T) {
	for _, rule := range []string{"Bash", "Bash(*)", "Bash(doomer *)", "Bash(doomer task submit *)"} {
		t.Run(rule, func(t *testing.T) {
			isolate(t)
			t.Setenv("HOME", t.TempDir())
			path, _ := claudePermissionPath()
			os.MkdirAll(filepath.Dir(path), 0700)
			original, _ := json.Marshal(map[string]any{"permissions": map[string]any{"deny": []string{rule}}, "theme": "dark"})
			os.WriteFile(path, original, 0600)
			s, _ := managedStore()
			p := inspectClaudePermissions()
			if p.State != "denied" || len(p.DeniedRules) != 1 {
				t.Fatal(p)
			}
			if err := installClaudePermissions(s); err == nil {
				t.Fatal("allow installation overrode an explicit deny")
			}
			after, _ := os.ReadFile(path)
			if string(after) != string(original) {
				t.Fatal("deny settings changed")
			}
		})
	}
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
	if r := run("--decline"); r.ClaudePermissions.Decision != "declined" || !r.UploadApproved {
		t.Fatal(r)
	}
	if r := run(); r.ClaudePermissions.Decision != "declined" || len(r.ClaudePermissions.InstallCommand) != 0 {
		t.Fatal(r)
	}
	if r := run("--allow"); r.ClaudePermissions.State != "configured" {
		t.Fatal(r)
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

func TestClaudePermissionFailureRemovesOnlyRedundantBackup(t *testing.T) {
	for _, failure := range []string{"settings", "receipt", "rollback"} {
		t.Run(failure, func(t *testing.T) {
			isolate(t)
			t.Setenv("HOME", t.TempDir())
			path, _ := claudePermissionPath()
			original := []byte(`{"theme":"dark"}`)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			s, err := managedStore()
			if err != nil {
				t.Fatal(err)
			}
			failed := errors.New("injected write failure")
			writes := 0
			writeSettings := func(path string, before, after []byte, mode os.FileMode) error {
				writes++
				if failure == "settings" || (failure == "rollback" && writes == 2) {
					return failed
				}
				return resetWriteFile(path, before, after, mode)
			}
			writeReceipt := func(path string, value any) error {
				if failure != "settings" {
					return failed
				}
				t.Fatal("receipt write must not follow failed settings write")
				return nil
			}
			err = installClaudePermissionsWithWriters(s, writeSettings, writeReceipt)
			if !errors.Is(err, failed) {
				t.Fatalf("lost write failure: %v", err)
			}
			current, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			backups, err := filepath.Glob(path + ".before-doomer-permissions-*")
			if err != nil {
				t.Fatal(err)
			}
			if failure == "rollback" {
				if string(current) == string(original) || len(backups) != 1 {
					t.Fatal("lost recovery backup after failed rollback", backups)
				}
				backup, err := os.ReadFile(backups[0])
				if err != nil || string(backup) != string(original) {
					t.Fatal("recovery backup is not the original", err)
				}
				return
			}
			if string(current) != string(original) || len(backups) != 0 {
				t.Fatalf("failed install left residue: %q, %v", current, backups)
			}
			if err := installClaudePermissions(s); err != nil {
				t.Fatal(err)
			}
			backups, _ = filepath.Glob(path + ".before-doomer-permissions-*")
			if len(backups) != 1 {
				t.Fatalf("retry must retain exactly one original backup: %v", backups)
			}
		})
	}
}

func TestClaudePermissionResetExplainsForeignHomeCleanup(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	otherPath := filepath.Join(t.TempDir(), ".claude", "settings.json")
	original := []byte(`{"permissions":{"allow":["Bash(doomer task submit *)"]}}`)
	if err := os.MkdirAll(filepath.Dir(otherPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(otherPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(s.Root, "claude-permissions", watch.Key(otherPath)+".json")
	if err := watch.Write(receiptPath, claudePermissionReceipt{ConfigPath: otherPath, Added: []string{claudeReviewRules[0]}}); err != nil {
		t.Fatal(err)
	}
	plan := &resetPlan{states: map[string]bool{}, repos: map[string]bool{}}
	plan.discoverState(filepath.Dir(s.Root))
	found := false
	for _, entry := range plan.Entries {
		if entry.Kind != "Claude permissions" {
			continue
		}
		found = true
		if entry.Path != otherPath || entry.Status != "manual_cleanup_required" || entry.apply != nil || !strings.Contains(entry.Error, claudeReviewRules[0]) || !strings.Contains(entry.Error, receiptPath) {
			t.Fatalf("unhelpful or unsafe cleanup entry: %+v", entry)
		}
	}
	if !found {
		t.Fatal("foreign-home cleanup was not reported")
	}
	current, err := os.ReadFile(otherPath)
	if err != nil || string(current) != string(original) {
		t.Fatal("modified foreign settings", err)
	}
}
