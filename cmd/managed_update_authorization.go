package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/doomerlabs/doomer/internal/managed"
	"github.com/doomerlabs/doomer/internal/watch"
)

type updateAuthorization struct {
	Authorized bool      `json:"authorized"`
	ApprovedAt time.Time `json:"approved_at"`
}

func updateAuthorizationKey(task managed.Task) string {
	scope := task.RepositoryID
	if scope == "" {
		scope = task.Bootstrap.Path
	}
	if task.Bootstrap.Scope == "personal" {
		scope = filepath.Clean(filepath.Join(filepath.Dir(task.Bootstrap.Path), "../../.."))
	}
	value, _ := json.Marshal(struct {
		Scope, Kind, API, Profile string
		Policy                    managed.Policy
	}{scope, task.Bootstrap.Scope, task.API, task.Profile, task.Bootstrap.Policy})
	return watch.Key(string(value))
}

// Authorization follows the repository and policy, while selections, rejected
// releases and rollback history remain specific to each checkout's wrapper.
func authorizeManagedUpdates(ctx context.Context, s managed.Store, task managed.Task, allow bool, saved selectedRelease) (bool, error) {
	if task.Repo != "" && task.RepositoryID != "" {
		actual, err := watch.RepositoryIdentity(ctx, task.Repo)
		if err != nil {
			return false, err
		}
		if actual != task.RepositoryID {
			return false, fmt.Errorf("repository identity changed before update authorization")
		}
	}
	key := updateAuthorizationKey(task)
	unlock, err := watchLock(s.Root, "update-authorization-"+key)
	if err != nil {
		return false, err
	}
	defer unlock()
	path := filepath.Join(s.Root, "update-authorizations", key+".json")
	var grant updateAuthorization
	err = watch.Read(path, &grant)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if allow {
		grant = updateAuthorization{Authorized: true, ApprovedAt: time.Now().UTC()}
	} else if err == nil {
		return grant.Authorized, nil
	} else {
		// Preserve previously approved standalone/test installations that have
		// no repository identity. Real repository grants require saved task
		// evidence tying the old path-based selection to this service/profile.
		grant.Authorized = task.RepositoryID == "" && saved.Authorized && saved.Policy == task.Bootstrap.Policy
		if !grant.Authorized {
			grant.Authorized, err = legacyManagedUpdateApproval(s, task)
			if err != nil {
				return false, err
			}
		}
		if !grant.Authorized {
			return false, nil
		}
		grant.ApprovedAt = time.Now().UTC()
	}
	if err := watch.Write(path, grant); err != nil {
		return false, err
	}
	return grant.Authorized, nil
}

func legacyManagedUpdateApproval(s managed.Store, task managed.Task) (bool, error) {
	files, err := filepath.Glob(filepath.Join(s.Root, "tasks", "*.json"))
	if err != nil {
		return false, err
	}
	for _, path := range files {
		var previous managed.Task
		if err := watch.Read(path, &previous); err != nil {
			continue
		}
		if previous.Release == "" || previous.API != task.API || previous.Profile != task.Profile || previous.Bootstrap.Scope != task.Bootstrap.Scope || previous.Bootstrap.Policy != task.Bootstrap.Policy {
			continue
		}
		if updateAuthorizationKey(previous) != updateAuthorizationKey(task) {
			continue
		}
		var selection selectedRelease
		if err := watch.Read(filepath.Join(s.Root, "selections", watch.Key(previous.Bootstrap.Path)+".json"), &selection); err != nil {
			continue
		}
		if selection.Authorized && selection.Policy == task.Bootstrap.Policy {
			return true, nil
		}
	}
	return false, nil
}
