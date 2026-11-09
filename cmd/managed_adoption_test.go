package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func adoptionFixture(t *testing.T) (managed.Store, managed.Task, *managedConsentEvidence) {
	t.Helper()
	s := managed.Store{Root: filepath.Join(t.TempDir(), "managed")}
	handle, _ := managed.NewHandle()
	task := managed.Task{Schema: managed.Protocol, Handle: handle, AccountID: "account", API: "https://example.test/api", Profile: "default", Agent: "codex", RepositoryID: "/private/repo/.git", Release: "2026.11.8.6", Purpose: "setup"}
	if err := recordIntegrationConsent(s, task, "browser_integration_approval", true); err != nil {
		t.Fatal(err)
	}
	e, err := savedManagedConsentEvidence(s, task)
	if err != nil || e == nil {
		t.Fatal(e, err)
	}
	if err := bindManagedAdoption(s, &task, e); err != nil {
		t.Fatal(err)
	}
	return s, task, e
}

func TestManagedAdoptionApprovalIdentitySurvivesTasksRepositoriesAndUpgrades(t *testing.T) {
	s, task, e := adoptionFixture(t)
	other := task
	other.Handle, _ = managed.NewHandle()
	other.RepositoryID, other.Release = "/other/repo/.git", "2026.11.9.1"
	other.HookActivated = true
	if err := bindManagedAdoption(s, &other, e); err != nil {
		t.Fatal(err)
	}
	if other.InstallationID != task.InstallationID || other.InstallationAttemptID != task.InstallationAttemptID || !other.HookActivated {
		t.Fatal("reuse changed the attempt or erased a verified hook", task, other)
	}
	for _, current := range []managed.Task{task, other} {
		if err := queueManagedAdoption(s, current, e); err != nil {
			t.Fatal(err)
		}
	}
	files := pendingManagedAdoption(s, task, e)
	if len(files) != 3 { // Two repository approvals, one successful hook; one cohort ID.
		t.Fatal(files)
	}
	for _, file := range files {
		var record managedAdoptionRecord
		if err := watch.Read(file, &record); err != nil {
			t.Fatal(err)
		}
		wire, _ := json.Marshal(record.Event)
		if record.Event.AttemptID != task.InstallationAttemptID || strings.Contains(string(wire), "/private/") || strings.Contains(string(wire), ".git") {
			t.Fatal("invalid or identifying event", string(wire))
		}
	}
	// A new grant represents another install even on the same machine.
	next := *e
	next.ApprovedAt = e.ApprovedAt.Add(time.Second)
	other.HookActivated = true
	if err := bindManagedAdoption(s, &other, &next); err != nil {
		t.Fatal(err)
	}
	if other.InstallationID != task.InstallationID || other.InstallationAttemptID == task.InstallationAttemptID || other.HookActivated {
		t.Fatal("reinstall did not create an independent unactivated attempt")
	}
}

func TestManagedAdoptionRequiresMatchingCompletedReview(t *testing.T) {
	for _, kind := range []string{"pending", "failed", "old_attempt", "ready"} {
		t.Run(kind, func(t *testing.T) {
			s, task, e := adoptionFixture(t)
			task.HookActivated = true
			task.Submission = &adversarylabs.WorkspaceSubmission{Client: map[string]string{"installation_id": task.InstallationID, "installation_attempt_id": task.InstallationAttemptID}}
			task.Review = &adversarylabs.WorkspaceReview{ID: "00000000-0000-0000-0000-000000000001", Early: &adversarylabs.WorkspaceAssessment{Status: "ready"}}
			switch kind {
			case "pending":
				task.Review.Early.Status = "pending"
			case "failed":
				task.Review.Early.FailureCode = "model_request_failed"
			case "old_attempt":
				task.Submission.Client["installation_attempt_id"] = strings.Repeat("f", 64)
			}
			if err := queueManagedAdoption(s, task, e); err != nil {
				t.Fatal(err)
			}
			want := 2
			if kind == "ready" {
				want = 3
			}
			if got := len(pendingManagedAdoption(s, task, e)); got != want {
				t.Fatalf("events = %d, want %d", got, want)
			}
		})
	}
}

type adoptionClient struct {
	events []adversarylabs.ActivationEvent
	fail   bool
}

func (c *adoptionClient) ReportActivation(_ context.Context, _ string, event adversarylabs.ActivationEvent) error {
	c.events = append(c.events, event)
	if c.fail {
		return errors.New("offline")
	}
	return nil
}

func TestManagedAdoptionRetriesDurablyAndDoesNotDuplicateAcknowledgedEvents(t *testing.T) {
	s, task, e := adoptionFixture(t)
	if err := queueManagedAdoption(s, task, e); err != nil {
		t.Fatal(err)
	}
	files := pendingManagedAdoption(s, task, e)
	client := &adoptionClient{fail: true}
	for range 4 {
		flushManagedAdoption(context.Background(), s, files, "token", client)
		var record managedAdoptionRecord
		if err := watch.Read(files[0], &record); err != nil || record.Sent || record.RetryAt.IsZero() {
			t.Fatal(record, err)
		}
		if pending := pendingManagedAdoption(s, task, e); len(pending) != 0 {
			t.Fatal("ignored retry delay")
		}
		record.RetryAt = time.Time{}
		if err := watch.Write(files[0], record); err != nil {
			t.Fatal(err)
		}
	}
	// Recreate the store and queue after a process restart; the event is retained.
	s = managed.Store{Root: s.Root}
	if err := queueManagedAdoption(s, task, e); err != nil {
		t.Fatal(err)
	}
	client.fail = false
	flushManagedAdoption(context.Background(), s, pendingManagedAdoption(s, task, e), "token", client)
	flushManagedAdoption(context.Background(), s, files, "token", client)
	if len(client.events) != 5 || len(pendingManagedAdoption(s, task, e)) != 0 {
		t.Fatal("lost or duplicated a report", client.events)
	}
	for _, event := range client.events {
		if event != client.events[0] {
			t.Fatal("retry changed event identity or timestamp")
		}
	}
	for _, mutate := range []func(*managed.Task){func(t *managed.Task) { t.AccountID = "other" }, func(t *managed.Task) { t.Profile = "other" }, func(t *managed.Task) { t.API = "https://other.test" }} {
		other := task
		mutate(&other)
		if len(pendingManagedAdoption(s, other, e)) != 0 {
			t.Fatal("crossed credential scope")
		}
	}
}

func TestManagedAdoptionResetDoesNotReplayEarlierApprovals(t *testing.T) {
	s, task, e := adoptionFixture(t)
	if err := queueManagedAdoption(s, task, e); err != nil {
		t.Fatal(err)
	}
	if err := watch.Write(filepath.Join(s.Root, "reset.json"), e.ApprovedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(pendingManagedAdoption(s, task, e)) != 0 {
		t.Fatal("reset revived an earlier approval")
	}
	next := *e
	next.ApprovedAt = e.ApprovedAt.Add(2 * time.Second)
	if err := bindManagedAdoption(s, &task, &next); err != nil {
		t.Fatal(err)
	}
	if err := queueManagedAdoption(s, task, &next); err != nil {
		t.Fatal(err)
	}
	if len(pendingManagedAdoption(s, task, &next)) != 1 {
		t.Fatal("fresh approval after reset was not independently reported")
	}
}

func TestManagedAdoptionReportsSetupAndStopsAfterRevocationOrAccountChange(t *testing.T) {
	s, task, _ := adoptionFixture(t)
	var events []adversarylabs.ActivationEvent
	account := task.AccountID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/whoami":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": account, "type": "user"})
		case "/v2/installations/activate":
			var event adversarylabs.ActivationEvent
			if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
				t.Error(err)
			}
			events = append(events, event)
			_, _ = w.Write([]byte(`{"accepted":true}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	task.API = server.URL
	if err := recordIntegrationConsent(s, task, "browser_integration_approval", true); err != nil {
		t.Fatal(err)
	}
	e, _ := savedManagedConsentEvidence(s, task)
	if err := bindManagedAdoption(s, &task, e); err != nil {
		t.Fatal(err)
	}
	credentials := adversarylabs.ConfigStore{Path: filepath.Join(filepath.Dir(s.Root), "config.json")}
	if err := credentials.SetAuth(adversarylabs.AuthKey(task.API, task.Profile), adversarylabs.Auth{Token: "test-token"}); err != nil {
		t.Fatal(err)
	}
	reportManagedAdoption(context.Background(), s, task, e)
	if len(events) != 1 || events[0].Stage != "approved" {
		t.Fatal("setup-only approval was not reported", events)
	}
	task.HookActivated = true
	task.Paused = true
	reportManagedAdoption(context.Background(), s, task, e)
	task.Paused = false
	account = "other-account"
	reportManagedAdoption(context.Background(), s, task, e)
	account = task.AccountID
	if err := recordIntegrationConsent(s, task, "revoked", false); err != nil {
		t.Fatal(err)
	}
	reportManagedAdoption(context.Background(), s, task, e)
	if len(events) != 1 {
		t.Fatal("reported under paused, different-account, or revoked access", events)
	}
}

func TestManagedAdoptionRetainsLegacyRepositoryConsentScope(t *testing.T) {
	s := managed.Store{Root: filepath.Join(t.TempDir(), "managed")}
	handle, err := managed.NewHandle()
	if err != nil {
		t.Fatal(err)
	}
	task := managed.Task{Schema: managed.Protocol, Handle: handle, AccountID: "account", API: "https://example.test/api", Profile: "default", Agent: "codex", RepositoryID: "/private/repo/.git", Release: "2026.11.8.6"}
	if err := recordManagedConsent(s, task, "repository_approval"); err != nil {
		t.Fatal(err)
	}
	evidence, err := savedManagedConsentEvidence(s, task)
	if err != nil || evidence == nil || evidence.AllRepositories || evidence.Repository != task.RepositoryID {
		t.Fatalf("invalid repository evidence: %+v %v", evidence, err)
	}
	if err := bindManagedAdoption(s, &task, evidence); err != nil {
		t.Fatal(err)
	}
	if err := queueManagedAdoption(s, task, evidence); err != nil {
		t.Fatal(err)
	}
	if pending := pendingManagedAdoption(s, task, evidence); len(pending) != 1 {
		t.Fatalf("repository-scoped event dropped: %v", pending)
	}
	other := task
	other.RepositoryID = "/other/repo/.git"
	if evidence, err := savedManagedConsentEvidence(s, other); err != nil || evidence != nil {
		t.Fatalf("repository grant crossed scope: %+v %v", evidence, err)
	}
}

func TestManagedAdoptionRepairsPartialIdentityFromVerifiedApproval(t *testing.T) {
	for _, missing := range []string{"installation", "attempt", "both"} {
		t.Run(missing, func(t *testing.T) {
			s, task, evidence := adoptionFixture(t)
			installation, attempt := task.InstallationID, task.InstallationAttemptID
			if missing != "attempt" {
				task.InstallationID = ""
			}
			if missing != "installation" {
				task.InstallationAttemptID = ""
			}
			if err := bindManagedAdoption(s, &task, evidence); err != nil {
				t.Fatal(err)
			}
			if task.InstallationID != installation || task.InstallationAttemptID != attempt {
				t.Fatal("identity not repaired from approval", task)
			}
		})
	}
}
