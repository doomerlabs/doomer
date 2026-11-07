package cmd

import (
	"fmt"
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
	AllRepositories bool      `json:"all_repositories,omitempty"`
	Agent           string    `json:"agent,omitempty"`
}

func managedConsentPath(s managed.Store, t managed.Task) string {
	key := watch.Key(t.AccountID + "\x00" + t.RepositoryID + "\x00" + t.API + "\x00" + t.Profile)
	return filepath.Join(s.Root, "consents", key+".json")
}

func (grant repositoryConsent) matches(t managed.Task) bool {
	scopeMatches := grant.Repository == t.RepositoryID
	if grant.AllRepositories {
		scopeMatches = grant.Agent != "" && grant.Agent == t.Agent
	}
	return grant.Approved && scopeMatches && grant.Account == t.AccountID && grant.API == t.API && grant.Profile == t.Profile
}

func integrationConsentPath(s managed.Store, t managed.Task) string {
	key := watch.Key(t.AccountID + "\x00" + t.API + "\x00" + t.Profile + "\x00" + t.Agent)
	return filepath.Join(s.Root, "integration-consents", key+".json")
}

func recordIntegrationConsent(s managed.Store, t managed.Task, source string, approved bool) error {
	if t.Agent != "codex" && t.Agent != "claude" {
		return fmt.Errorf("integration approval requires a codex or claude agent")
	}
	grant := repositoryConsent{Account: t.AccountID, API: t.API, Profile: t.Profile,
		Agent: t.Agent, AllRepositories: true, Approved: approved, At: time.Now().UTC(), Source: source}
	return watch.Write(integrationConsentPath(s, t), grant)
}

// A revoked integration grant suppresses older repository grants. A later,
// explicit repository approval can still authorize that repository alone.
func readManagedConsent(s managed.Store, t managed.Task) (repositoryConsent, string, error) {
	var integration repositoryConsent
	path := integrationConsentPath(s, t)
	err := watch.Read(path, &integration)
	if err != nil && !os.IsNotExist(err) {
		return repositoryConsent{}, path, err
	}
	if err == nil && integration.matches(t) {
		return integration, path, nil
	}
	var grant repositoryConsent
	path = managedConsentPath(s, t)
	if err := watch.Read(path, &grant); err != nil {
		return grant, path, err
	}
	if !integration.At.IsZero() && !grant.At.After(integration.At) {
		grant.Approved = false
	}
	return grant, path, nil
}

func recordManagedConsent(s managed.Store, t managed.Task, source string) error {
	grant := repositoryConsent{Account: t.AccountID, Repository: t.RepositoryID,
		API: t.API, Profile: t.Profile, Approved: true, At: time.Now().UTC(), Source: source}
	return watch.Write(managedConsentPath(s, t), grant)
}

func savedManagedConsentEvidence(s managed.Store, t managed.Task) (*managedConsentEvidence, error) {
	grant, file, err := readManagedConsent(s, t)
	if os.IsNotExist(err) {
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
		Includes: []string{"repository_snapshots", "task_context", "finding_responses"}, LinkedWorktrees: true,
		AllRepositories: grant.AllRepositories, Agent: grant.Agent}, nil
}
