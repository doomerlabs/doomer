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
