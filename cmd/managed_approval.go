package cmd

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

type pendingManagedApproval struct {
	Input      adversarylabs.ManagedSetupRequest `json:"input"`
	Credential string                            `json:"credential"`
	Verifier   string                            `json:"verifier"`
	URL        string                            `json:"url"`
	Expires    time.Time                         `json:"expires"`
}

func displayManagedService(api string) string {
	parsed, err := url.Parse(api)
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimSuffix(api, "/api")
}

func managedApproval(c *cobra.Command, app *application.App, s managed.Store, t *managed.Task) error {
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, t.API)
	authKey := adversarylabs.AuthKey(t.API, t.Profile)
	expectedAuth, hadAuth, err := app.Dependencies().Auth.StoredAuthE(authKey)
	if err != nil {
		return err
	}
	auth, account, identityErr := managedIdentity(c.Context(), app, *t)
	if identityErr != nil && !errors.Is(identityErr, errManagedLoginRequired) {
		return identityErr
	}
	if identityErr == nil {
		if t.AccountID != "" && t.AccountID != account {
			return fmt.Errorf("task belongs to another account")
		}
		t.AccountID = account
		approved, err := managedConsent(s, *t, auth.Token, false)
		if err != nil {
			return err
		}
		if approved && (!t.Portable || t.PortableApproved || t.PortableDeclined) {
			if t.PortableApproved {
				if err := savePortable(c.Context(), app, s, *t, false); err != nil {
					return err
				}
			}
			t.Stage = "ready"
			if err = s.Save(t); err != nil {
				return err
			}
			r := resultFor(*t)
			r.Authenticated, r.UploadApproved, r.Instructions = true, true, managed.Instructions
			return outputManaged(c, s, *t, r)
		}
	}
	file := filepath.Join(s.Root, "approvals", t.Handle+".json")
	var pending pendingManagedApproval
	if err := watch.Read(file, &pending); err != nil && !os.IsNotExist(err) {
		return err
	}
	if pending.Input.Request == "" || time.Now().After(pending.Expires) || pending.Input.PortableState != t.Portable {
		secret := make([]byte, 96)
		if _, err := rand.Read(secret); err != nil {
			return err
		}
		pending = pendingManagedApproval{Verifier: base64.RawURLEncoding.EncodeToString(secret[:32]),
			Credential: auth.Token, Expires: time.Now().Add(30 * time.Minute)}
		if identityErr != nil {
			pending.Credential = "doomer_anon_" + base64.RawURLEncoding.EncodeToString(secret[64:])
		}
		challenge := sha256.Sum256([]byte(pending.Verifier))
		repos, err := managedApprovalRepositories(s, *t)
		if err != nil {
			return err
		}
		pending.Input = adversarylabs.ManagedSetupRequest{Resumable: true, PortableState: t.Portable, Request: base64.RawURLEncoding.EncodeToString(secret[32:64]),
			CodeChallenge: base64.RawURLEncoding.EncodeToString(challenge[:]), Profile: t.Profile, API: t.API,
			Repositories: repos, Agents: []string{t.Agent}}
		// Persist the request identity before network I/O so a retry cannot create
		// another approval when the first response was lost.
		if err = watch.Write(file, pending); err != nil {
			return err
		}
	}
	if pending.URL == "" {
		created, err := client.CreateManagedSetup(c.Context(), pending.Credential, pending.Input)
		if err != nil {
			return managed.NetworkError("repository_approval", err)
		}
		page, err := url.Parse(created.URL)
		api, apiErr := url.Parse(t.API)
		if err != nil || apiErr != nil || page.Scheme != api.Scheme || page.Host != api.Host || page.User != nil || page.Path != "/cli/setup" || page.RawQuery != "" || page.Fragment != "request="+pending.Input.Request {
			return fmt.Errorf("invalid repository approval URL")
		}
		pending.URL = created.URL
		if err = watch.Write(file, pending); err != nil {
			return err
		}
	}
	result, err := client.PollManagedSetup(c.Context(), pending.Input.Request, pending.Verifier)
	if err != nil {
		return managed.NetworkError("repository_approval", err)
	}
	r := resultFor(*t)
	r.Authenticated = identityErr == nil
	switch result.State {
	case "pending":
		r.State, r.URL = "approval_required", pending.URL
		r.Action = "Open this Doomer repository approval page to review this repository, its linked worktrees, and previous grants. Sign in there if needed. After choosing Allow, resume doomer prepare --task " + t.Handle
	case "cancelled":
		r.State, r.Action = "approval_declined", "Repository approval was cancelled. Doomer uploads remain disabled."
	case "approved":
		if result.Token == "" || result.AccountID == "" {
			return fmt.Errorf("repository approval has no authenticated account")
		}
		if t.AccountID != "" && t.AccountID != result.AccountID {
			return fmt.Errorf("repository approval belongs to another account")
		}
		verified, err := client.Whoami(c.Context(), result.Token)
		if err != nil {
			return managed.NetworkError("account_verification", err)
		}
		verifiedAccount := verified.ID
		if verifiedAccount != result.AccountID {
			return fmt.Errorf("repository approval account mismatch")
		}
		if err = app.Dependencies().Auth.SetAuthCAS(authKey, expectedAuth, hadAuth, adversarylabs.Auth{Token: result.Token, ClientID: result.ClientID, ExpiresAt: result.ExpiresAt, RegistryNamespace: result.RegistryNamespace}); err != nil {
			return err
		}

		t.AccountID = verifiedAccount
		if err = recordManagedConsent(s, *t, "browser_repository_approval"); err != nil {
			return err
		}
		if t.Portable {
			if result.PortableState == nil {
				return fmt.Errorf("the service did not confirm portable setup approval")
			}
			if *result.PortableState {
				if err = savePortable(c.Context(), app, s, *t, true); err != nil {
					return err
				}
			}
			t.PortableApproved = *result.PortableState
			t.PortableDeclined = !*result.PortableState
		}
		r.State, r.Authenticated, r.UploadApproved, r.Instructions = "ready", true, true, managed.Instructions
	default:
		return fmt.Errorf("invalid repository approval state")
	}
	t.Stage = r.State
	if err = s.Save(t); err != nil {
		return err
	}
	return outputManaged(c, s, *t, r)
}

func managedApprovalRepositories(s managed.Store, t managed.Task) ([]adversarylabs.ManagedSetupRepository, error) {
	main := t.Repo
	if filepath.Base(t.RepositoryID) == ".git" {
		main = filepath.Dir(t.RepositoryID)
	}
	repos := []adversarylabs.ManagedSetupRepository{{MainCheckout: main, Checkout: t.Repo}}
	files, err := filepath.Glob(filepath.Join(s.Root, "consents", "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, file := range files {
		var grant repositoryConsent
		if err := watch.Read(file, &grant); err != nil {
			return nil, err
		}
		if !grant.Approved || grant.Account != t.AccountID || grant.API != t.API || grant.Profile != t.Profile {
			continue
		}
		if grant.Repository == t.RepositoryID {
			repos[0].AlreadyGranted = true
			continue
		}
		path := grant.Repository
		if filepath.Base(path) == ".git" {
			path = filepath.Dir(path)
		}
		repos = append(repos, adversarylabs.ManagedSetupRepository{MainCheckout: path, Checkout: path, AlreadyGranted: true})
	}
	return repos, nil
}
