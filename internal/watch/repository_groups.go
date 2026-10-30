package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Git's common directory identifies one repository across all linked checkouts.
func RepositoryIdentity(ctx context.Context, path string) (string, error) {
	raw, err := filesystemGit(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(raw))
	if canonical, err := filepath.EvalSymlinks(id); err == nil {
		id = canonical
	}
	return filepath.Clean(id), nil
}

func repositoryIdentity(path string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	id, err := RepositoryIdentity(ctx, path)
	if err != nil {
		return path
	}
	return id
}

type repositorySelection struct {
	ID      string `json:"id"`
	Removed bool   `json:"removed"`
}

func SetRepositorySelection(root, path string, removed bool) error {
	var repo Repository
	_ = Read(repositoryFile(root, path), &repo)
	id := repo.ID
	if id == "" {
		id = repositoryIdentity(path)
	}
	return Write(filepath.Join(root, "repository-selection", Key(id)+".json"), repositorySelection{ID: id, Removed: removed})
}

func groupSelection(root, id string) (repositorySelection, bool) {
	var selection repositorySelection
	err := Read(filepath.Join(root, "repository-selection", Key(id)+".json"), &selection)
	return selection, err == nil && selection.ID == id
}

type WatchRuntime struct {
	Session string `json:"session"`
	Token   string `json:"token"`
	Status  string `json:"status"`
}

func SetWatchRuntime(root, path string, runtime WatchRuntime) error {
	return Locked(root, "runtime-"+Key(path), func() error { return Write(filepath.Join(root, "watching", Key(path)+".json"), runtime) })
}

func ClearWatchRuntime(root, path, token string) {
	file := filepath.Join(root, "watching", Key(path)+".json")
	var runtime WatchRuntime
	_ = Locked(root, "runtime-"+Key(path), func() error {
		if Read(file, &runtime) == nil && runtime.Token == token {
			return os.Remove(file)
		}
		return nil
	})
}

func RepositoryStatus(root string, repo Repository, now time.Time) string {
	if repo.Removed {
		return "Removed"
	}
	if !repo.Active(now) {
		return "Inactive"
	}
	var heartbeat time.Time
	var session string
	var runtime WatchRuntime
	if Read(filepath.Join(root, "heartbeat.json"), &heartbeat) == nil && now.Sub(heartbeat) >= 0 && now.Sub(heartbeat) < 3*time.Second &&
		Read(filepath.Join(root, "watch-session.json"), &session) == nil && Read(filepath.Join(root, "watching", Key(repo.Path)+".json"), &runtime) == nil && runtime.Session == session {
		return runtime.Status
	}
	return "Not running"
}

func CheckoutLabel(ctx context.Context, path string) string {
	raw, err := filesystemGit(ctx, path, "symbolic-ref", "--quiet", "--short", "HEAD").Output()
	if err == nil && len(raw) > 0 {
		return strings.TrimSpace(string(raw))
	}
	raw, err = filesystemGit(ctx, path, "rev-parse", "--short", "HEAD").Output()
	if err == nil {
		return "detached " + strings.TrimSpace(string(raw))
	}
	return filepath.Base(path)
}
