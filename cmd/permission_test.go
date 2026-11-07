package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

type permissionFailingWriter struct {
	bytes.Buffer
	err error
}

func TestHasPermissionReusesIntegrationAcrossRepositories(t *testing.T) {
	store := isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/whoami" || r.Header.Get("Authorization") != "Bearer integration-token" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.Write([]byte(`{"id":"account"}`))
	}))
	defer server.Close()
	if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: "integration-token"}); err != nil {
		t.Fatal(err)
	}
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	task := managed.Task{AccountID: "account", Agent: "codex", API: server.URL, Profile: "default"}
	if err := recordIntegrationConsent(s, task, "explicit_integration_approval", true); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{newWatchTestRepo(t), newWatchTestRepo(t)} {
		for _, agent := range []string{"codex", "claude"} {
			output, err := execute(t, "", "--api-url", server.URL, "has-permission", "--managed", "--agent", agent, "--json", repo)
			var result struct {
				Approved bool                    `json:"approved"`
				Consent  *managedConsentEvidence `json:"consent"`
			}
			if err != nil || json.Unmarshal([]byte(output), &result) != nil || result.Approved != (agent == "codex") {
				t.Fatal(agent, output, err)
			}
			if result.Approved && (result.Consent == nil || !result.Consent.AllRepositories) {
				t.Fatal("missing integration evidence", output)
			}
		}
	}
}

func TestHasPermissionManagedRequiresVerifiedAccount(t *testing.T) {
	for _, credential := range []string{"missing", "anonymous", "rejected"} {
		t.Run(credential, func(t *testing.T) {
			store := isolate(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			if credential != "missing" {
				token := "rejected-token"
				if credential == "anonymous" {
					token = adversarylabs.AnonymousTokenPrefix + "test"
				}
				if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: token}); err != nil {
					t.Fatal(err)
				}
			}
			repo := newWatchTestRepo(t)
			for _, format := range []string{"plain", "json"} {
				args := []string{"--api-url", server.URL, "has-permission", "--managed", repo}
				if format == "json" {
					args = append(args, "--json")
				}
				output, err := execute(t, "", args...)
				if !errors.Is(err, errManagedLoginRequired) || output != "" {
					t.Fatalf("unverified account became a consent decision: %q %v", output, err)
				}
			}
		})
	}
}

func TestHasPermissionManagedUsesCurrentAccountAndPreservesState(t *testing.T) {
	store := isolate(t)
	account := "account"
	unavailable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/whoami" || r.Header.Get("Authorization") != "Bearer managed-token" {
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if unavailable {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": account, "type": "user"})
	}))
	defer server.Close()
	if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: "managed-token"}); err != nil {
		t.Fatal(err)
	}
	repo := newWatchTestRepo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	reviewGit(t, repo, "worktree", "add", "--detach", linked, "HEAD")
	id, err := watching.RepositoryIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	location, err := managedStoreLocation()
	if err != nil {
		t.Fatal(err)
	}
	output, err := execute(t, "", "--api-url", server.URL, "has-permission", "--managed", repo)
	if err != nil || output != "false\n" {
		t.Fatalf("missing managed store: %s %v", output, err)
	}
	if _, err := os.Stat(location.Root); !os.IsNotExist(err) {
		t.Fatal("permission check created managed state", err)
	}
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	task := managed.Task{AccountID: account, RepositoryID: id, API: server.URL, Profile: "default"}
	if err := recordManagedConsent(s, task, "browser_repository_approval"); err != nil {
		t.Fatal(err)
	}
	file := managedConsentPath(s, task)
	before, _ := os.ReadFile(file)
	// A read-only check must not normalize the directory's permissions.
	if err := os.Chmod(s.Root, 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{repo, linked} {
		output, err := execute(t, "", "--api-url", server.URL, "has-permission", "--managed", "--json", path)
		var result struct {
			Approved bool                    `json:"approved"`
			Consent  *managedConsentEvidence `json:"consent"`
		}
		if err != nil || json.Unmarshal([]byte(output), &result) != nil || !result.Approved || result.Consent == nil || result.Consent.Account != account || !strings.Contains(output, "finding_responses") {
			t.Fatalf("managed consent: %s %v", output, err)
		}
		if strings.Contains(output, "managed-token") {
			t.Fatal("exposed credentials")
		}
	}
	after, _ := os.ReadFile(file)
	info, _ := os.Stat(s.Root)
	if string(before) != string(after) || info.Mode().Perm() != 0755 {
		t.Fatal("permission check changed saved state")
	}
	account = "other-account"
	output, err = execute(t, "", "--api-url", server.URL, "has-permission", "--managed", repo)
	if err != nil || output != "false\n" {
		t.Fatalf("accepted another account's grant: %s %v", output, err)
	}
	account = task.AccountID
	if err := watching.Write(file, repositoryConsent{Account: account}); err != nil {
		t.Fatal(err)
	}
	output, err = execute(t, "", "--api-url", server.URL, "has-permission", "--managed", repo)
	if err != nil || output != "false\n" {
		t.Fatalf("accepted revoked grant: %s %v", output, err)
	}
	unavailable = true
	output, err = execute(t, "", "--api-url", server.URL, "has-permission", "--managed", repo)
	if err == nil || output != "" {
		t.Fatalf("network failure became a consent decision: %s %v", output, err)
	}
}

func (w *permissionFailingWriter) Write(p []byte) (int, error) {
	n, _ := w.Buffer.Write(p[:len(p)/2])
	return n, w.err
}

func TestHasPermissionReportsOutputFailure(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	for _, format := range []string{"plain", "json"} {
		t.Run(format, func(t *testing.T) {
			failure := errors.New("output unavailable")
			output := &permissionFailingWriter{err: failure}
			command := NewRootCommand()
			command.SetOut(output)
			args := []string{"has-permission", repo}
			if format == "json" {
				args = append(args, "--json")
			}
			command.SetArgs(args)
			if err := command.Execute(); !errors.Is(err, failure) {
				t.Fatalf("output failure returned %v", err)
			}
		})
	}
}

type permissionNetworkGuard struct{ t *testing.T }

func (g permissionNetworkGuard) RoundTrip(*http.Request) (*http.Response, error) {
	g.t.Error("permission check contacted the server")
	return nil, errors.New("unexpected network request")
}

func TestHasPermissionReusesRepositoryGrantWithoutSetup(t *testing.T) {
	store := isolate(t)
	transport := http.DefaultTransport
	http.DefaultTransport = permissionNetworkGuard{t}
	t.Cleanup(func() { http.DefaultTransport = transport })
	api, profile, token := "https://permission.example/api", "default", "saved-test-token"
	if err := store.SetAuth(adversarylabs.AuthKey(api, profile), adversarylabs.Auth{Token: token}); err != nil {
		t.Fatal(err)
	}
	main := newWatchTestRepo(t)
	linked := filepath.Join(t.TempDir(), "new-worktree")
	reviewGit(t, main, "worktree", "add", "--detach", linked, "HEAD")
	linked, err := watching.Root(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	id, err := watching.RepositoryIdentity(context.Background(), main)
	if err != nil {
		t.Fatal(err)
	}
	root, file, err := consentPaths(api, profile)
	if err != nil {
		t.Fatal(err)
	}
	// Consent survives old timestamps, absent runtimes/hooks, and pauses.
	cfg := watching.Config{Version: 1, API: api, Profile: profile, AccountKey: watching.Key(token), ExplicitConsent: true,
		ConsentedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), Directories: []string{main}, RepositoryIDs: []string{id}}
	if err := watching.Write(file, cfg); err != nil {
		t.Fatal(err)
	}
	if err := watching.Write(filepath.Join(root, "paused.json"), true); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(store.Path), 0755); err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string]string {
		t.Helper()
		files := map[string]string{}
		err := filepath.WalkDir(os.Getenv("DOOMER_CONFIG_DIR"), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			var content []byte
			if !entry.IsDir() {
				content, err = os.ReadFile(path)
			}
			files[path] = fmt.Sprintf("%s:%s", info.Mode(), content)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return files
	}
	before := snapshot()
	for _, repo := range []string{main, linked} {
		out, err := execute(t, "", "--api-url", api, "has-permission", repo, "--json")
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Approved     bool   `json:"approved"`
			RepositoryID string `json:"repository_id"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil || !result.Approved || result.RepositoryID != id {
			t.Fatalf("permission for %s: %s %v", repo, out, err)
		}
		if strings.Contains(out, token) {
			t.Fatal("permission output exposed credentials")
		}
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("permission check changed local files or permissions")
	}
	for _, tc := range []struct {
		name   string
		change func(*watching.Config)
		repo   string
		want   string
	}{
		{"different repository", func(*watching.Config) {}, newWatchTestRepo(t), "false\n"},
		{"checkout-only main", func(c *watching.Config) { c.RepositoryIDs = nil }, main, "true\n"},
		{"checkout-only linked", func(c *watching.Config) { c.RepositoryIDs = nil }, linked, "false\n"},
		{"different account", func(c *watching.Config) { c.AccountKey = watching.Key("another") }, main, "false\n"},
		{"different profile", func(c *watching.Config) { c.Profile = "other" }, main, "false\n"},
		{"different service", func(c *watching.Config) { c.API = "https://other.example/api" }, main, "false\n"},
		{"revoked", func(c *watching.Config) { c.Directories = nil; c.RepositoryIDs = nil }, main, "false\n"},
		{"all repositories", func(c *watching.Config) { c.AllRepositories = true }, linked, "true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := cfg
			tc.change(&changed)
			if err := watching.Write(file, changed); err != nil {
				t.Fatal(err)
			}
			out, err := execute(t, "", "--api-url", api, "has-permission", tc.repo)
			if err != nil || out != tc.want {
				t.Fatalf("got %q %v, want %q", out, err, tc.want)
			}
		})
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if out, err := execute(t, "", "--api-url", api, "has-permission", main); err != nil || out != "false\n" {
		t.Fatalf("missing grant: %q %v", out, err)
	}
	if err := os.WriteFile(file, []byte("broken JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := execute(t, "", "--api-url", api, "has-permission", main); err == nil || out != "" {
		t.Fatalf("corrupt grant returned a permission decision: %q %v", out, err)
	}
}
