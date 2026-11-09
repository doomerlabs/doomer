package cmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

// The approval receipt, not the task, repository, token, or CLI release, owns
// the attempt. Existing receipts acquire an ID without inventing a new grant.
func managedAttemptID(e *managedConsentEvidence) string {
	return watch.Key(strings.Join([]string{"managed-approval-v1", e.Account, e.Service, e.Profile, e.Repository, e.Agent, e.ApprovedAt.UTC().Format(time.RFC3339Nano)}, "\x00"))
}

func bindManagedAdoption(s managed.Store, t *managed.Task, e *managedConsentEvidence) error {
	installation, err := watch.EnsureInstallationID(filepath.Join(s.Root, "identities", watch.Key(t.API+"\x00"+t.Profile)))
	if err != nil {
		return err
	}
	attempt := managedAttemptID(e)
	if t.InstallationID == installation && t.InstallationAttemptID == attempt {
		return nil
	}
	previous := *t
	t.InstallationID, t.InstallationAttemptID = installation, attempt
	// A hook from an older approval is not evidence for this approval.
	t.HookActivated = false
	if err := s.Save(t); err != nil {
		*t = previous
		return err
	}
	return nil
}

type managedAdoptionRecord struct {
	Account, API, Profile, RepositoryID string
	ApprovedAt                          time.Time
	Event                               adversarylabs.ActivationEvent
	Sent                                bool
	RetryAt                             time.Time
}

func managedAdoptionDir(s managed.Store) string { return filepath.Join(s.Root, "adoption") }

func queueManagedAdoption(s managed.Store, t managed.Task, e *managedConsentEvidence) error {
	if t.InstallationID == "" || t.InstallationAttemptID != managedAttemptID(e) || t.Paused {
		return nil
	}
	client := "codex-terminal"
	if t.Agent == "claude" {
		client = "claude-code"
	} else if os.Getenv("CODEX_INTERNAL_ORIGINATOR_OVERRIDE") == "Codex Desktop" {
		client = "codex-desktop"
	}
	// A scoped opaque identifier avoids sending paths or reading repository
	// content during setup. Review verification uses the explicit client IDs.
	event := adversarylabs.ActivationEvent{Stage: "approved", AttemptID: t.InstallationAttemptID,
		InstallationID: t.InstallationID, Repository: "local/" + watch.Key(t.InstallationID+"\x00"+t.RepositoryID),
		Agent: t.Agent, Client: client, OS: runtime.GOOS, Architecture: runtime.GOARCH,
		CLIVersion: t.Release, OccurredAt: time.Now().UTC().Format(time.RFC3339)}
	events := []adversarylabs.ActivationEvent{event}
	if t.HookActivated {
		event.Stage = "activated"
		events = append(events, event)
	}
	// Never credit an old task's review to a replacement approval. The API also
	// verifies this identity and the completed assessment before accepting it.
	if t.Review != nil && t.Review.Early != nil && t.Review.Early.Status == "ready" && t.Review.Early.FailureCode == "" &&
		t.Submission != nil && t.Submission.Client["installation_id"] == t.InstallationID &&
		t.Submission.Client["installation_attempt_id"] == t.InstallationAttemptID {
		event.Stage, event.ReviewID = "first_review", t.Review.ID
		events = append(events, event)
	}
	for _, event := range events {
		key := watch.Key(strings.Join([]string{t.AccountID, t.API, t.Profile, event.InstallationID, event.AttemptID, t.RepositoryID, t.Agent, event.Stage}, "\x00"))
		err := watch.Locked(s.Root, "adoption-"+key, func() error {
			file := filepath.Join(managedAdoptionDir(s), key+".json")
			var previous managedAdoptionRecord
			if err := watch.Read(file, &previous); err == nil {
				return nil
			} else if !os.IsNotExist(err) {
				return err
			}
			return watch.Write(file, managedAdoptionRecord{Account: t.AccountID, API: t.API, Profile: t.Profile, RepositoryID: t.RepositoryID, ApprovedAt: e.ApprovedAt, Event: event})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

type managedAdoptionClient interface {
	ReportActivation(context.Context, string, adversarylabs.ActivationEvent) error
}

func pendingManagedAdoption(s managed.Store, t managed.Task, e *managedConsentEvidence) []string {
	entries, _ := os.ReadDir(managedAdoptionDir(s))
	cutoff, err := resetCutoff(s)
	if err != nil {
		return nil
	}
	type candidate struct {
		file  string
		retry time.Time
	}
	var candidates []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		file := filepath.Join(managedAdoptionDir(s), entry.Name())
		var record managedAdoptionRecord
		if watch.Read(file, &record) != nil || record.Sent || time.Now().Before(record.RetryAt) ||
			(!cutoff.IsZero() && !record.ApprovedAt.After(cutoff)) ||
			record.Account != t.AccountID || record.API != t.API || record.Profile != t.Profile ||
			(e.AllRepositories && record.Event.Agent != e.Agent) || (!e.AllRepositories && record.RepositoryID != e.Repository) {
			continue
		}
		candidates = append(candidates, candidate{file, record.RetryAt})
	}
	// An invalid old event must not starve newly queued approvals.
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].retry.Before(candidates[j].retry) })
	var pending []string
	for _, candidate := range candidates[:min(6, len(candidates))] {
		pending = append(pending, candidate.file)
	}
	return pending
}

// Each CLI call retries a bounded batch. Failed reports remain durable instead
// of being discarded after a small number of transient network failures.
func flushManagedAdoption(ctx context.Context, s managed.Store, files []string, token string, client managedAdoptionClient) {
	for _, file := range files {
		if ctx.Err() != nil {
			return
		}
		_ = watch.Locked(s.Root, "adoption-"+strings.TrimSuffix(filepath.Base(file), ".json"), func() error {
			var record managedAdoptionRecord
			if err := watch.Read(file, &record); err != nil || record.Sent || time.Now().Before(record.RetryAt) {
				return err
			}
			record.RetryAt = time.Now().Add(time.Minute)
			if err := watch.Write(file, record); err != nil {
				return err
			}
			if err := client.ReportActivation(ctx, token, record.Event); err != nil {
				return err
			}
			record.Sent = true
			return watch.Write(file, record)
		})
	}
}

func reportManagedAdoption(ctx context.Context, s managed.Store, t managed.Task, e *managedConsentEvidence) {
	if t.Paused || t.AccountID == "" || t.Release == "" || t.Release == "dev" || (t.Agent != "codex" && t.Agent != "claude") {
		return
	}
	// Recheck the live receipt, including reset/revocation, before queuing or
	// sending. A saved task does not itself grant permission to report.
	current, err := savedManagedConsentEvidence(s, t)
	if err != nil || current == nil || managedAttemptID(current) != managedAttemptID(e) {
		return
	}
	if queueManagedAdoption(s, t, e) != nil {
		return
	}
	files := pendingManagedAdoption(s, t, e)
	if len(files) == 0 {
		return
	}
	store := adversarylabs.ConfigStore{Path: filepath.Join(filepath.Dir(s.Root), "config.json")}
	auth, ok, err := store.StoredAuthE(adversarylabs.AuthKey(t.API, t.Profile))
	if err != nil || !ok || auth.Token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, t.API)
	if adversarylabs.IsAnonymousToken(auth.Token) {
		if t.AccountID != "anonymous:"+watch.Key(auth.Token) {
			return
		}
	} else {
		account, err := client.Whoami(ctx, auth.Token)
		if err != nil || account.ID != t.AccountID {
			return
		}
	}
	flushManagedAdoption(ctx, s, files, auth.Token, client)
}
