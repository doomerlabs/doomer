package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestFailedPortableRequestLeavesExistingTaskResumable(t *testing.T) {
	isolate(t)
	repo := newWatchTestRepo(t)
	id, err := watch.RepositoryIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: managed.Protocol, Handle: handle, Repo: repo, RepositoryID: id, API: "https://doomer.ai/api", Profile: "default", Paused: true}
	if err = s.Save(&task); err != nil {
		t.Fatal(err)
	}
	api, profile := task.API, task.Profile
	command := newPrepareCommand(nil, &api, &profile)
	command.SetArgs([]string{"--task", handle, "--portable"})
	if err = command.Execute(); err == nil {
		t.Fatal("accepted portable setup without origin")
	}
	saved, err := s.Load(handle)
	if err != nil || saved.Portable {
		t.Fatal("failed request changed saved task", saved, err)
	}
	command = newPrepareCommand(nil, &api, &profile)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"--task", handle})
	if err = command.Execute(); err != nil {
		t.Fatal("ordinary retry failed", err)
	}
}

func TestPortableSetupAcrossHomesAndMountPaths(t *testing.T) {
	for _, integration := range []bool{false, true} {
		name := "repository"
		if integration {
			name = "integration"
		}
		t.Run(name, func(t *testing.T) {
			isolate(t)
			ctx := context.Background()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "Bearer other-account-token" {
					w.Write([]byte(`{"id":"other-account"}`))
					return
				}
				if r.Header.Get("Authorization") != "Bearer account-token" {
					t.Error("unexpected credential")
				}
				w.Write([]byte(`{"id":"account"}`))
			}))
			defer server.Close()
			repo := newWatchTestRepo(t)
			reviewGit(t, repo, "remote", "add", "origin", "https://github.com/example/project.git")
			id, err := watch.RepositoryIdentity(ctx, repo)
			if err != nil {
				t.Fatal(err)
			}
			store := adversarylabs.ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
			app := &application.App{Deps: application.Dependencies{Auth: store}}
			auth := adversarylabs.Auth{Token: "account-token", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
			if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "default"), auth); err != nil {
				t.Fatal(err)
			}
			s := managed.Store{Root: filepath.Join(t.TempDir(), "managed")}
			handle, _ := managed.NewHandle()
			policy := managed.Policy{Protocol: 2, Channel: "stable"}
			task := managed.Task{Schema: 2, Handle: handle, Repo: repo, RepositoryID: id, API: server.URL,
				Profile: "default", AccountID: "account", Agent: "codex", Portable: true, PortableApproved: true,
				Bootstrap: managed.Bootstrap{Path: filepath.Join(repo, ".claude/skills/doomer-review/SKILL.md"), Policy: policy}}
			if integration {
				if err := recordIntegrationConsent(s, task, "browser_integration_approval", true); err != nil {
					t.Fatal(err)
				}
			} else if err := recordManagedConsent(s, task, "browser_repository_approval"); err != nil {
				t.Fatal(err)
			}
			if err := watch.Write(filepath.Join(s.Root, "selections", watch.Key(task.Bootstrap.Path)+".json"), selectedRelease{Policy: policy, Authorized: true}); err != nil {
				t.Fatal(err)
			}
			if err := watch.Write(filepath.Join(s.Root, "update-authorizations", updateAuthorizationKey(task)+".json"), updateAuthorization{Authorized: true, ApprovedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			if err := savePortable(ctx, app, s, task, true); err != nil {
				t.Fatal(err)
			}
			path, _, err := portableLocation(ctx, task)
			if err != nil {
				t.Fatal(err)
			}
			original, err := readPortable(path)
			if err != nil {
				t.Fatal(err)
			}

			// The same shared Git storage is mounted at a new path with an empty home.
			moved := filepath.Join(t.TempDir(), "mounted-checkout")
			if err := os.Rename(repo, moved); err != nil {
				t.Fatal(err)
			}
			task.Repo = moved
			task.RepositoryID, err = watch.RepositoryIdentity(ctx, moved)
			if err != nil {
				t.Fatal(err)
			}
			task.Bootstrap.Path = filepath.Join(moved, ".claude/skills/doomer-review/SKILL.md")
			task.AccountID, task.Portable, task.PortableApproved = "", false, false
			fresh := adversarylabs.ConfigStore{Path: filepath.Join(t.TempDir(), "config.json")}
			app.Deps.Auth = fresh
			s = managed.Store{Root: filepath.Join(t.TempDir(), "managed")}
			if err := restorePortable(ctx, app, s, &task); err != nil {
				t.Fatal(err)
			}
			got, exists, err := fresh.StoredAuthE(adversarylabs.AuthKey(server.URL, "default"))
			if err != nil || !exists || got != auth || !task.PortableApproved {
				t.Fatal("account was not restored", exists, err)
			}
			consent, err := savedManagedConsentEvidence(s, task)
			if err != nil || consent == nil || consent.AllRepositories != integration || (!integration && consent.Repository != task.RepositoryID) || !consent.ApprovedAt.Equal(original.Grant.At) {
				t.Fatal("original grant was not preserved across the mount change", consent, err)
			}
			var selection selectedRelease
			if err := watch.Read(filepath.Join(s.Root, "selections", watch.Key(task.Bootstrap.Path)+".json"), &selection); err != nil || !selection.Authorized {
				t.Fatal("update policy was not restored", err)
			}
			var updates updateAuthorization
			if err := watch.Read(filepath.Join(s.Root, "update-authorizations", updateAuthorizationKey(task)+".json"), &updates); err != nil || !updates.Authorized || !updates.ApprovedAt.Equal(original.UpdateApprovedAt) {
				t.Fatal("update approval evidence was not preserved", err)
			}
			if err := checkPortable(ctx, task); err != nil {
				t.Fatal(err)
			}
			reviewGit(t, moved, "remote", "set-url", "origin", "https://github.com/example/different.git")
			if err := restorePortable(ctx, app, s, &task); err == nil {
				t.Fatal("restored access after the repository remote changed")
			}
			reviewGit(t, moved, "remote", "set-url", "origin", "https://github.com/example/project.git")
			if err := fresh.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: "other-account-token"}); err != nil {
				t.Fatal(err)
			}
			if err := restorePortable(ctx, app, s, &task); err == nil {
				t.Fatal("replaced a different signed-in account")
			}
			if err := fresh.SetAuth(adversarylabs.AuthKey(server.URL, "default"), auth); err != nil {
				t.Fatal(err)
			}
			task.PortableApproved = true
			if err := removePortable(ctx, task); err != nil {
				t.Fatal(err)
			}
			if err := checkPortable(ctx, task); err == nil {
				t.Fatal("revoked portable approval remained usable")
			}
			if err := savePortable(ctx, app, s, task, false); err == nil {
				t.Fatal("a concurrent refresh recreated revoked portable access")
			}
			if err := savePortable(ctx, app, s, task, true); err != nil {
				t.Fatal(err)
			}
			path, _, err = portableLocation(ctx, task)
			if err != nil {
				t.Fatal(err)
			}
			portable, err := readPortable(path)
			if err != nil {
				t.Fatal(err)
			}
			portable.Grant.At = time.Now().Add(24 * time.Hour)
			if err := watch.Write(path, portable); err != nil {
				t.Fatal(err)
			}
			if err := recordIntegrationConsent(s, task, "integration_revoked", false); err != nil {
				t.Fatal(err)
			}
			revoked, err := os.ReadFile(integrationConsentPath(s, task))
			if err != nil {
				t.Fatal(err)
			}
			if !integration {
				if err := os.Remove(managedConsentPath(s, task)); err != nil {
					t.Fatal(err)
				}
			}
			if err := restorePortable(ctx, app, s, &task); err != nil || task.PortableApproved {
				t.Fatal("restored portable approval after integration revocation", err)
			}
			after, err := os.ReadFile(integrationConsentPath(s, task))
			if err != nil || !bytes.Equal(revoked, after) {
				t.Fatal("portable restoration changed revocation evidence", err)
			}
			if evidence, err := savedManagedConsentEvidence(s, task); err != nil || evidence != nil {
				t.Fatal("revoked integration became usable", evidence, err)
			}
			if !integration {
				if _, err := os.Stat(managedConsentPath(s, task)); !os.IsNotExist(err) {
					t.Fatal("restored an older repository grant after integration revocation", err)
				}
				if err := recordManagedConsent(s, task, "explicit_cli_approval"); err != nil {
					t.Fatal(err)
				}
				if err := restorePortable(ctx, app, s, &task); err != nil || !task.PortableDeclined {
					t.Fatal("stale portable record blocked a later local approval", err)
				}
				if evidence, err := savedManagedConsentEvidence(s, task); err != nil || evidence == nil {
					t.Fatal("later local approval was lost", evidence, err)
				}
			}
		})
	}
}

func TestPortableSetupRejectsPublicOrSymlinkedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portable.json")
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPortable(path); err == nil {
		t.Fatal("accepted world-readable credentials")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readPortable(link); err == nil {
		t.Fatal("accepted symlinked credentials")
	}
}

func TestPortableSetupCommandUsesTheSelectedRuntime(t *testing.T) {
	task := managed.Task{Handle: "task", API: "https://doomer.ai/api", Profile: "work", Release: "2026.11.6.9", RuntimeHome: t.TempDir()}
	command := resultFor(task).PortableSetupCommand
	if len(command) != 9 || command[0] == "doomer" || command[7] != task.Handle || command[8] != "--portable" {
		t.Fatalf("incorrect portable setup command: %v", command)
	}
	task.Portable = true
	if len(resultFor(task).PortableSetupCommand) != 0 {
		t.Fatal("already-portable task requested another setup")
	}
}

func TestManagedRevokeSurvivesPortableRemovalFailure(t *testing.T) {
	isolate(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"account"}`))
	}))
	defer server.Close()
	repo := newWatchTestRepo(t) // No origin remote, so portable cleanup cannot resolve it.
	id, err := watch.RepositoryIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	store := adversarylabs.ConfigStore{Path: filepath.Join(os.Getenv("DOOMER_CONFIG_DIR"), "config.json")}
	if err := store.SetAuth(adversarylabs.AuthKey(server.URL, "default"), adversarylabs.Auth{Token: "token"}); err != nil {
		t.Fatal(err)
	}
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: 2, Handle: handle, Repo: repo, RepositoryID: id, API: server.URL, Profile: "default", AccountID: "account", Portable: true, PortableApproved: true}
	if err := s.Save(&task); err != nil {
		t.Fatal(err)
	}
	if err := recordManagedConsent(s, task, "browser_repository_approval"); err != nil {
		t.Fatal(err)
	}
	output, err := execute(t, "", "--api-url", server.URL, "task", "revoke", handle)
	if err != nil {
		t.Fatal(err)
	}
	var result managedResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "consent_revoked" || result.UploadApproved || result.Error == "" || result.Action == "" {
		t.Fatal("partial cleanup did not report local revocation and recovery guidance", result)
	}
	approved, err := managedConsent(s, task, "token", false)
	if err != nil || approved {
		t.Fatal("portable cleanup failure preserved local upload permission", approved, err)
	}
}
