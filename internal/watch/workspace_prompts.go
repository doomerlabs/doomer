package watch

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A scope records a prompt's initial local state, never uploaded source. A
// parent-rooted chat only activates repositories that change during that turn.
type WorkspacePrompt struct {
	Owner            Owner             `json:"owner"`
	Directory        string            `json:"directory"`
	DirectRepository string            `json:"direct_repository,omitempty"`
	Started          time.Time         `json:"started"`
	Baselines        map[string]string `json:"baselines"`
	ClosingAt        time.Time         `json:"closing_at,omitempty"`
}

func workspacePromptPath(root, session string) string {
	return filepath.Join(root, "workspace-prompts", Key(session)+".json")
}

// DiscoverRepositories follows directory nesting without a depth limit. Git
// metadata and dependency/cache trees are excluded; symlinks are not traversed.
func DiscoverRepositories(ctx context.Context, directory string) ([]string, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	if repo, err := Root(ctx, directory); err == nil {
		found[repo] = true
	}
	err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		if path != directory {
			switch entry.Name() {
			case ".git", "node_modules", ".venv", "__pycache__", ".cache":
				return filepath.SkipDir
			}
		}
		if info, err := os.Lstat(filepath.Join(path, ".git")); err == nil && info.Mode()&os.ModeSymlink == 0 {
			if repo, err := Root(ctx, path); err == nil && repo == path {
				found[repo] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	repos := make([]string, 0, len(found))
	for repo := range found {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	return repos, nil
}

func beginWorkspacePrompt(ctx context.Context, root string, cfg Config, input HookInput, owner Owner) error {
	directory, err := filepath.Abs(input.CWD)
	if err != nil {
		return err
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	repos, err := DiscoverRepositories(ctx, directory)
	if err != nil {
		return err
	}
	direct, _ := Root(ctx, directory)
	scope := WorkspacePrompt{Owner: owner, Directory: directory, DirectRepository: direct, Started: time.Now(), Baselines: map[string]string{}}
	for _, repo := range repos {
		if !Allowed(cfg, repo) || RepositoryRemoved(root, repo) {
			continue
		}
		digest, err := snapshotDigest(ctx, repo)
		if err != nil {
			return fmt.Errorf("capture initial state for %s: %w", repo, err)
		}
		scope.Baselines[repo] = digest
	}
	return Locked(root, "workspace-"+owner.Session, func() error {
		var current Turn
		if Read(TurnPath(root, owner.Session), &current) != nil || current.Owner != owner {
			return nil
		}
		if err := Write(workspacePromptPath(root, owner.Session), scope); err != nil {
			return err
		}
		for repo := range scope.Baselines {
			if err := RegisterRepository(root, repo, "workspace prompt", false, time.Now()); err != nil {
				return err
			}
		}
		return nil
	})
}

func endWorkspacePrompt(root string, owner Owner) {
	_ = Locked(root, "workspace-"+owner.Session, func() error {
		var scope WorkspacePrompt
		path := workspacePromptPath(root, owner.Session)
		if Read(path, &scope) == nil && scope.Owner == owner {
			scope.ClosingAt = time.Now()
			return Write(path, scope)
		}
		return nil
	})
}

func WorkspacePrompts(root string) ([]WorkspacePrompt, error) {
	entries, err := os.ReadDir(filepath.Join(root, "workspace-prompts"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var scopes []WorkspacePrompt
	for _, entry := range entries {
		var scope WorkspacePrompt
		var turn Turn
		if Read(filepath.Join(root, "workspace-prompts", entry.Name()), &scope) != nil || scope.Owner.Session == "" || Read(TurnPath(root, scope.Owner.Session), &turn) != nil || turn.Owner != scope.Owner {
			continue
		}
		if !scope.ClosingAt.IsZero() {
			pending := false
			for _, repo := range turn.Repositories {
				var feed Feed
				if Read(FeedPath(root, repo), &feed) != nil || feed.Owner != scope.Owner || (feed.AssessmentStatus != "ready" && feed.AssessmentStatus != "unavailable") {
					pending = true
				}
			}
			// Stop drains the final queued snapshot, but never leaves a dead turn
			// watching indefinitely after the native review deadline.
			if !pending || time.Since(scope.ClosingAt) > 6*time.Minute {
				continue
			}
		}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

// Called by the coordinator to discover repositories created during a turn.
func RefreshWorkspacePrompts(ctx context.Context, root string, cfg Config) (map[string]bool, int, error) {
	scopes, err := WorkspacePrompts(root)
	if err != nil {
		return nil, 0, err
	}
	active := map[string]bool{}
	for _, scope := range scopes {
		repos, err := DiscoverRepositories(ctx, scope.Directory)
		if err != nil {
			return nil, 0, err
		}
		if !scope.ClosingAt.IsZero() {
			var turn Turn
			_ = Read(TurnPath(root, scope.Owner.Session), &turn)
			repos = turn.Repositories
		}
		for _, repo := range repos {
			if !Allowed(cfg, repo) || RepositoryRemoved(root, repo) {
				continue
			}
			active[repo] = true
			if err := RegisterRepository(root, repo, "workspace prompt", false, time.Now()); err != nil {
				return nil, 0, err
			}
		}
	}
	return active, len(scopes), nil
}

func workspacePromptForRepository(root, path string) *WorkspacePrompt {
	scopes, err := WorkspacePrompts(root)
	if err != nil {
		return nil
	}
	var chosen *WorkspacePrompt
	for i := range scopes {
		scope := &scopes[i]
		if !scope.ClosingAt.IsZero() {
			var state State
			if Read(StatePath(root, path), &state) != nil || state.Owner != scope.Owner {
				continue
			}
		}
		_, existing := scope.Baselines[path]
		if !existing {
			rel, err := filepath.Rel(scope.Directory, path)
			if err != nil || rel == ".." || len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator) {
				continue
			}
		}
		if chosen == nil || scope.Started.After(chosen.Started) {
			chosen = scope
		}
	}
	return chosen
}

func (r Runner) bindWorkspacePrompt(path, fingerprint string) bool {
	chosen := workspacePromptForRepository(r.Root, path)
	if chosen == nil {
		return false
	}
	if !chosen.ClosingAt.IsZero() {
		var state State
		if Read(StatePath(r.Root, path), &state) != nil || state.Fingerprint != fingerprint {
			return false
		}
	}
	var previous baselineRequest
	_ = Read(baselineRequestPath(r.Root, path), &previous)
	if chosen.DirectRepository != path && chosen.Baselines[path] == fingerprint && previous.Owner != chosen.Owner {
		return false
	}
	if previous.Owner != chosen.Owner {
		requestBaseline(r.Root, path, chosen.Owner)
	}
	return true
}

func queueWorkspaceStop(ctx context.Context, root string, cfg Config, owner Owner) error {
	var scope WorkspacePrompt
	if Read(workspacePromptPath(root, owner.Session), &scope) != nil || scope.Owner != owner {
		return nil
	}
	repos, err := DiscoverRepositories(ctx, scope.Directory)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		if !Allowed(cfg, repo) || RepositoryRemoved(root, repo) {
			continue
		}
		capture, err := captureForReview(ctx, root, repo, "")
		if err != nil {
			if strings.Contains(err.Error(), "snapshot contains no changes") {
				continue
			}
			return err
		}
		if _, err := (Runner{Root: root, Config: cfg}).queueAllChanges(ctx, repo, capture); err != nil {
			return err
		}
	}
	return nil
}
