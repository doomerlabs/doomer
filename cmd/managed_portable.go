package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/oci"
)

// This opt-in record lives outside the worktree and is never a review input.
// Its browser approval explicitly covers sharing account access with other
// execution environments that can access this checkout's Git metadata.
type portableSetup struct {
	Schema     int                `json:"schema"`
	API        string             `json:"api"`
	Profile    string             `json:"profile"`
	Origin     string             `json:"origin"`
	Auth       adversarylabs.Auth `json:"auth"`
	Grant      repositoryConsent  `json:"grant"`
	Policy     managed.Policy     `json:"policy"`
	Authorized bool               `json:"authorized"`
}

func portableLocation(ctx context.Context, t managed.Task) (string, string, error) {
	id, err := watch.RepositoryIdentity(ctx, t.Repo)
	if err != nil || id != t.RepositoryID {
		return "", "", fmt.Errorf("portable setup checkout identity changed")
	}
	out, err := exec.CommandContext(ctx, "git", "-C", t.Repo, "remote", "get-url", "origin").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "", "", fmt.Errorf("portable setup requires an origin remote")
	}
	dir := filepath.Join(id, "doomer", "portable")
	for _, path := range []string{filepath.Dir(dir), dir} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return "", "", fmt.Errorf("portable setup directory must be private and not symlinked")
		}
	}
	return filepath.Join(dir, watch.Key(t.API+"\x00"+t.Profile)+".json"), strings.TrimSpace(string(out)), nil
}

func readPortable(path string) (portableSetup, error) {
	var record portableSetup
	info, err := os.Lstat(path)
	if err != nil {
		return record, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return record, fmt.Errorf("portable setup file must be private and regular")
	}
	f, err := oci.OpenRegularNoFollow(path)
	if err != nil {
		return record, err
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, 64<<10)).Decode(&record)
	return record, err
}

func restorePortable(ctx context.Context, app *application.App, s managed.Store, t *managed.Task) error {
	if t.PortableDeclined {
		return nil
	}
	if !t.Portable {
		_, err := os.Lstat(filepath.Join(t.RepositoryID, "doomer", "portable"))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	path, origin, err := portableLocation(ctx, *t)
	if err != nil {
		return err
	}
	record, err := readPortable(path)
	if os.IsNotExist(err) {
		if t.PortableApproved {
			t.PortableApproved = false
		}
		return nil
	}
	if err != nil {
		return err
	}
	if record.Schema != 1 || record.API != t.API || record.Profile != t.Profile || record.Origin != origin || !record.Grant.Approved || record.Grant.Account == "" || record.Grant.API != t.API || record.Grant.Profile != t.Profile {
		return fmt.Errorf("portable setup does not match this repository, service, and profile")
	}
	if record.Auth.Token == "" || adversarylabs.IsAnonymousToken(record.Auth.Token) {
		return fmt.Errorf("portable setup has no account credential")
	}
	t.Portable = true
	t.PortableApproved = false
	if record.Auth.ExpiresAt != "" {
		expires, err := time.Parse(time.RFC3339, record.Auth.ExpiresAt)
		if err != nil {
			return fmt.Errorf("portable setup has invalid credential expiration")
		}
		if !time.Now().Add(30 * time.Second).Before(expires) {
			// An expired record cannot authorize anything. A fresh browser decision
			// can replace it through the ordinary setup flow.
			return nil
		}
	}
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, t.API)
	identity, err := client.Whoami(ctx, record.Auth.Token)
	if err != nil {
		if errors.Is(err, adversarylabs.ErrNotAuthenticated) {
			return nil
		}
		return managed.NetworkError("portable_account_verification", err)
	}
	if identity.ID != record.Grant.Account {
		return fmt.Errorf("portable setup account mismatch")
	}
	if t.AccountID != "" && t.AccountID != identity.ID {
		return fmt.Errorf("portable setup belongs to a different task account")
	}
	key := adversarylabs.AuthKey(t.API, t.Profile)
	existing, existed, err := app.Dependencies().Auth.StoredAuthE(key)
	if err != nil {
		return err
	}
	local, account, err := managedIdentity(ctx, app, *t)
	if err == nil && account != identity.ID {
		return fmt.Errorf("portable setup belongs to a different account than this profile")
	}
	if err != nil && !errors.Is(err, errManagedLoginRequired) {
		return err
	}
	if local.Token == "" || err != nil {
		if err := app.Dependencies().Auth.SetAuthCAS(key, existing, existed, record.Auth); err != nil {
			return err
		}
	}
	t.AccountID = identity.ID
	grant := record.Grant
	grant.Repository = t.RepositoryID
	var previous repositoryConsent
	if err := watch.Read(managedConsentPath(s, *t), &previous); err == nil && !previous.Approved {
		t.Portable = true
		t.PortableApproved = false
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := watch.Write(managedConsentPath(s, *t), grant); err != nil {
		return err
	}
	if record.Authorized && record.Policy == t.Bootstrap.Policy {
		selectionPath := filepath.Join(s.Root, "selections", watch.Key(t.Bootstrap.Path)+".json")
		var selection selectedRelease
		if err := watch.Read(selectionPath, &selection); err != nil && !os.IsNotExist(err) {
			return err
		}
		if selection.Policy != record.Policy {
			selection = selectedRelease{Policy: record.Policy}
		}
		selection.Authorized = true
		if err := watch.Write(selectionPath, selection); err != nil {
			return err
		}
	}
	t.Portable, t.PortableApproved = true, true
	return s.Save(t)
}

func savePortable(ctx context.Context, app *application.App, s managed.Store, t managed.Task, allowCreate bool) error {
	path, origin, err := portableLocation(ctx, t)
	if err != nil {
		return err
	}
	auth, account, err := managedIdentity(ctx, app, t)
	if err != nil || account != t.AccountID {
		return fmt.Errorf("verify account before saving portable setup")
	}
	var grant repositoryConsent
	if err := watch.Read(managedConsentPath(s, t), &grant); err != nil {
		return err
	}
	if !grant.matches(t) {
		return fmt.Errorf("portable setup requires repository approval")
	}
	var selection selectedRelease
	if err := watch.Read(filepath.Join(s.Root, "selections", watch.Key(t.Bootstrap.Path)+".json"), &selection); err != nil {
		return err
	}
	if !selection.Authorized || selection.Policy != t.Bootstrap.Policy {
		return fmt.Errorf("portable setup requires an approved update policy")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return watch.Locked(filepath.Dir(path), "portable-setup", func() error {
		previous, err := readPortable(path)
		if os.IsNotExist(err) && !allowCreate {
			return fmt.Errorf("portable setup was revoked during preparation; request approval again")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && previous.Grant.Account != account {
			return fmt.Errorf("revoke the previous account's portable setup before replacing it")
		}
		return watch.Write(path, portableSetup{Schema: 1, API: t.API, Profile: t.Profile, Origin: origin, Auth: auth, Grant: grant, Policy: selection.Policy, Authorized: true})
	})
}

func removePortable(ctx context.Context, t managed.Task) error {
	path, _, err := portableLocation(ctx, t)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Dir(path)); os.IsNotExist(err) {
		return nil
	}
	return watch.Locked(filepath.Dir(path), "portable-setup", func() error {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	})
}

func checkPortable(ctx context.Context, t managed.Task) error {
	path, origin, err := portableLocation(ctx, t)
	if err != nil {
		return err
	}
	record, err := readPortable(path)
	if err != nil {
		return fmt.Errorf("portable repository approval is unavailable; resume prepare")
	}
	if record.Origin != origin || record.API != t.API || record.Profile != t.Profile || record.Grant.Account != t.AccountID || !record.Grant.Approved {
		return fmt.Errorf("portable repository approval changed; resume prepare")
	}
	return nil
}
