package watch

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

const RepositoryIdlePeriod = 14 * 24 * time.Hour

// Repository selection is separate from upload consent and scoped to its state root.
type Repository struct {
	ID           string    `json:"repository_id,omitempty"`
	Path         string    `json:"path"`
	Source       string    `json:"source"`
	Explicit     bool      `json:"explicit,omitempty"`
	Removed      bool      `json:"removed,omitempty"`
	LastActivity time.Time `json:"last_agent_activity,omitempty"`
}

func repositoryFile(root, path string) string {
	return filepath.Join(root, "checkouts", Key(path)+".json")
}

func RepositoryRemoved(root, path string) bool {
	var repo Repository
	_ = Read(repositoryFile(root, path), &repo)
	id := repo.ID
	if id == "" {
		id = repositoryIdentity(path)
	}
	if selection, ok := groupSelection(root, id); ok {
		return selection.Removed
	}
	return repo.Removed
}

func (r Repository) Active(now time.Time) bool {
	return !r.Removed && (r.Explicit || (!r.LastActivity.IsZero() && now.Sub(r.LastActivity) < RepositoryIdlePeriod))
}

// Discovery never overrides a user's explicit removal. Only add can do that.
func RegisterRepository(root, path, source string, explicit bool, activity time.Time) error {
	return Locked(root, "checkout-selection-"+Key(path), func() error {
		var repo Repository
		_ = Read(repositoryFile(root, path), &repo)
		previous := repo
		if repo.ID == "" {
			repo.ID = repositoryIdentity(path)
		}
		if selection, ok := groupSelection(root, repo.ID); ok {
			repo.Removed = selection.Removed
		}
		if repo.Removed && !explicit {
			return nil
		}
		if repo.Path == "" {
			repo = Repository{Path: path, Source: source}
		}
		if repo.ID == "" {
			repo.ID = repositoryIdentity(path)
		}
		if explicit {
			repo.Explicit, repo.Removed, repo.Source = true, false, "explicit"
		}
		if activity.After(repo.LastActivity) {
			repo.LastActivity = activity
		}
		if repo == previous {
			return nil
		}
		return Write(repositoryFile(root, path), repo)
	})
}

func RemoveRepository(root, path string) error {
	return Locked(root, "checkout-selection-"+Key(path), func() error {
		var repo Repository
		_ = Read(repositoryFile(root, path), &repo)
		repo.Path, repo.Removed, repo.Explicit = path, true, false
		return Write(repositoryFile(root, path), repo)
	})
}

func Repositories(root string, cfg Config) ([]Repository, error) {
	// Import the previous registration format once, without renewing idle time.
	for _, path := range cfg.Directories {
		if _, err := os.Stat(repositoryFile(root, path)); os.IsNotExist(err) {
			if err := RegisterRepository(root, path, "explicit", true, time.Time{}); err != nil {
				return nil, err
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "discovered"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		var path string
		if Read(filepath.Join(root, "discovered", entry.Name()), &path) != nil {
			continue
		}
		if _, err := os.Stat(repositoryFile(root, path)); !os.IsNotExist(err) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		activity := info.ModTime()
		var state State
		if Read(StatePath(root, path), &state) == nil && !state.RequestedAt.IsZero() {
			activity = state.RequestedAt
		}
		if err := RegisterRepository(root, path, "agent", false, activity); err != nil {
			return nil, err
		}
	}
	entries, err = os.ReadDir(filepath.Join(root, "checkouts"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var repos []Repository
	for _, entry := range entries {
		var repo Repository
		if Read(filepath.Join(root, "checkouts", entry.Name()), &repo) == nil && repo.Path != "" {
			if repo.ID == "" {
				id := repositoryIdentity(repo.Path)
				if err := Locked(root, "checkout-selection-"+Key(repo.Path), func() error {
					if err := Read(repositoryFile(root, repo.Path), &repo); err != nil {
						return err
					}
					if repo.ID == "" {
						repo.ID = id
						return Write(repositoryFile(root, repo.Path), repo)
					}
					return nil
				}); err != nil {
					return nil, err
				}
			}
			if selection, ok := groupSelection(root, repo.ID); ok {
				repo.Removed = selection.Removed
			}
			repos = append(repos, repo)
		}
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Path < repos[j].Path })
	return repos, nil
}
