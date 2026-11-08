package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
)

func TestManagedConsentEvidenceIncludesFindingResponsesAndSurvivesOldRecords(t *testing.T) {
	isolate(t)
	s, err := managedStore()
	if err != nil {
		t.Fatal(err)
	}
	task := managed.Task{AccountID: "account", RepositoryID: "/repo/.git", API: "https://doomer.ai/api", Profile: "default"}
	grant := repositoryConsent{Account: task.AccountID, Repository: task.RepositoryID, API: task.API, Profile: task.Profile, Approved: true, At: time.Now().UTC()}
	if err = watch.Write(managedConsentPath(s, task), grant); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(managedConsentPath(s, task))
	evidence, err := savedManagedConsentEvidence(s, task)
	if err != nil || evidence == nil || evidence.Source != "saved_repository_grant" || !evidence.LinkedWorktrees || !evidence.ApprovedAt.Equal(grant.At) {
		t.Fatal(evidence, err)
	}
	data, _ := json.Marshal(evidence)
	if !strings.Contains(string(data), "finding_responses") || strings.Contains(string(data), "token") {
		t.Fatal(string(data))
	}
	after, _ := os.ReadFile(managedConsentPath(s, task))
	if string(before) != string(after) {
		t.Fatal("reading evidence changed the saved grant")
	}
	for _, field := range []string{"account", "repository", "service", "profile"} {
		other := task
		switch field {
		case "account":
			other.AccountID = "another-account"
		case "repository":
			other.RepositoryID = "/another/.git"
		case "service":
			other.API = "https://another.example/api"
		case "profile":
			other.Profile = "another-profile"
		}
		if evidence, err = savedManagedConsentEvidence(s, other); err != nil || evidence != nil {
			t.Fatal(field, evidence, err)
		}
	}
	if err = recordManagedConsent(s, task, "browser_repository_approval"); err != nil {
		t.Fatal(err)
	}
	evidence, err = savedManagedConsentEvidence(s, task)
	if err != nil || evidence.Source != "browser_repository_approval" {
		t.Fatal(evidence, err)
	}
	grant.Approved = false
	if err = watch.Write(managedConsentPath(s, task), grant); err != nil {
		t.Fatal(err)
	}
	if evidence, err = savedManagedConsentEvidence(s, task); err != nil || evidence != nil {
		t.Fatal(evidence, err)
	}
}

func TestIntegrationConsentAcrossRepositoriesAndRevocation(t *testing.T) {
	s := managed.Store{Root: t.TempDir()}
	task := managed.Task{AccountID: "account", RepositoryID: "/first/.git", API: "https://doomer.ai/api", Profile: "default", Agent: "codex"}
	if err := recordManagedConsent(s, task, "legacy"); err != nil {
		t.Fatal(err)
	}
	other := task
	other.RepositoryID = "/second/.git"
	if evidence, err := savedManagedConsentEvidence(s, other); err != nil || evidence != nil {
		t.Fatal("legacy grant expanded", evidence, err)
	}
	if err := recordIntegrationConsent(s, task, "explicit_integration_approval", true); err != nil {
		t.Fatal(err)
	}
	for _, current := range []managed.Task{task, other} {
		evidence, err := savedManagedConsentEvidence(s, current)
		if err != nil || evidence == nil || !evidence.AllRepositories || evidence.Agent != "codex" || evidence.Repository != "" {
			t.Fatal("missing integration evidence", evidence, err)
		}
	}
	for _, field := range []string{"account", "service", "profile", "agent"} {
		changed := other
		switch field {
		case "account":
			changed.AccountID = "other"
		case "service":
			changed.API = "https://other.example/api"
		case "profile":
			changed.Profile = "other"
		case "agent":
			changed.Agent = "claude"
		}
		if evidence, err := savedManagedConsentEvidence(s, changed); err != nil || evidence != nil {
			t.Fatal("integration crossed scope", field, evidence, err)
		}
	}
	if err := recordIntegrationConsent(s, task, "integration_revoked", false); err != nil {
		t.Fatal(err)
	}
	var legacy repositoryConsent
	if err := watch.Read(managedConsentPath(s, task), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.At = time.Now().Add(24 * time.Hour)
	if err := watch.Write(managedConsentPath(s, task), legacy); err != nil {
		t.Fatal(err)
	}
	for _, current := range []managed.Task{task, other} {
		if evidence, err := savedManagedConsentEvidence(s, current); err != nil || evidence != nil {
			t.Fatal("revocation revived a prior grant", evidence, err)
		}
	}
	if err := recordManagedConsent(s, task, "explicit_cli_approval"); err != nil {
		t.Fatal(err)
	}
	if err := watch.Read(managedConsentPath(s, task), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.At = time.Now().Add(-24 * time.Hour)
	if err := watch.Write(managedConsentPath(s, task), legacy); err != nil {
		t.Fatal(err)
	}
	if evidence, err := savedManagedConsentEvidence(s, task); err != nil || evidence == nil || evidence.AllRepositories {
		t.Fatal("later repository approval unavailable", evidence, err)
	}
	if evidence, err := savedManagedConsentEvidence(s, other); err != nil || evidence != nil {
		t.Fatal("repository approval restored integration", evidence, err)
	}
	if err := recordIntegrationConsent(s, task, "integration_revoked", false); err != nil {
		t.Fatal(err)
	}
	if evidence, err := savedManagedConsentEvidence(s, task); err != nil || evidence != nil {
		t.Fatal("later revocation retained a repository exception", evidence, err)
	}
}

// Session, checkout, release, and hook diagnostics must never renew an existing
// integration grant or turn it into a fresh permission request.
func TestGrantReuseAcrossSessionAndCheckoutChanges(t *testing.T) {
	s := managed.Store{Root: t.TempDir()}
	original := managed.Task{AccountID: "account", RepositoryID: "repo/.git", Repo: "repo", API: "https://doomer.ai/api", Profile: "default", Agent: "codex", HookActivated: true}
	if err := recordIntegrationConsent(s, original, "explicit_integration_approval", true); err != nil {
		t.Fatal(err)
	}
	path := integrationConsentPath(s, original)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"session", "linked-worktree", "other-repository", "release", "hook-unverified"} {
		t.Run(change, func(t *testing.T) {
			next := original
			switch change {
			case "session":
				next.Capabilities.SessionID = "new-session"
			case "linked-worktree":
				next.Repo = "linked-checkout"
			case "other-repository":
				next.Repo = "other"
				next.RepositoryID = "other/.git"
			case "release":
				next.Release = "2026.11.8.1"
			case "hook-unverified":
				next.HookActivated = false
			}
			evidence, err := savedManagedConsentEvidence(s, next)
			if err != nil || evidence == nil || !evidence.AllRepositories {
				t.Fatal("matching grant was not reused", evidence, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatal("grant was renewed or changed", err)
			}
		})
	}
}
