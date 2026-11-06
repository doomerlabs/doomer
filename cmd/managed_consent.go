package cmd

import (
	"os"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
)

type managedConsentEvidence struct {
	Receipt         string    `json:"receipt"`
	Account         string    `json:"account"`
	Repository      string    `json:"repository"`
	Service         string    `json:"service"`
	Profile         string    `json:"profile"`
	ApprovedAt      time.Time `json:"approved_at"`
	Source          string    `json:"source"`
	Includes        []string  `json:"includes"`
	LinkedWorktrees bool      `json:"linked_worktrees"`
}

func managedConsentPath(s managed.Store, t managed.Task) string {
	key := watch.Key(t.AccountID + "\x00" + t.RepositoryID + "\x00" + t.API + "\x00" + t.Profile)
	return filepath.Join(s.Root, "consents", key+".json")
}

func (grant repositoryConsent) matches(t managed.Task) bool {
	return grant.Approved && grant.Account == t.AccountID && grant.Repository == t.RepositoryID && grant.API == t.API && grant.Profile == t.Profile
}

func recordManagedConsent(s managed.Store, t managed.Task, source string) error {
	grant := repositoryConsent{Account: t.AccountID, Repository: t.RepositoryID,
		API: t.API, Profile: t.Profile, Approved: true, At: time.Now().UTC(), Source: source}
	return watch.Write(managedConsentPath(s, t), grant)
}

func savedManagedConsentEvidence(s managed.Store, t managed.Task) (*managedConsentEvidence, error) {
	file := managedConsentPath(s, t)
	var grant repositoryConsent
	if err := watch.Read(file, &grant); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if !grant.matches(t) || grant.At.IsZero() {
		return nil, nil
	}
	source := grant.Source
	if source == "" {
		source = "saved_repository_grant"
	}
	return &managedConsentEvidence{Receipt: file, Account: grant.Account, Repository: grant.Repository,
		Service: grant.API, Profile: grant.Profile, ApprovedAt: grant.At, Source: source,
		Includes: []string{"repository_snapshots", "task_context", "finding_responses"}, LinkedWorktrees: true}, nil
}
