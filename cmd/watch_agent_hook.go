package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

// The manager owns installation and approval. Hooks read that data directly;
// neither starting a watcher nor delivering a result requires Node at runtime.
type agentHookState struct {
	Version    int               `json:"version"`
	API        string            `json:"api"`
	Identities map[string]string `json:"identities"`
	Grants     []struct {
		Scope      string    `json:"scope"`
		API        string    `json:"api"`
		Repo       string    `json:"repo"`
		Agents     []string  `json:"agents"`
		ApprovedAt time.Time `json:"approvedAt"`
	} `json:"grants"`
}

func agentHookAPI(explicit, environment, saved string) (string, error) {
	api := explicit
	if api == "" {
		api = environment
	}
	if api == "" {
		api = saved
	}
	if api == "" {
		api = adversarylabs.DefaultAPIURL
	}
	api = strings.TrimRight(api, "/")
	return api, adversarylabs.ValidateAPIURL(api)
}

func agentHookConsent(state agentHookState, api, agent, repo string) (watching.Config, error) {
	cfg := watching.Config{Version: 1, API: api, Profile: "node-" + agent, ExplicitConsent: true, AllChanges: true}
	for _, grant := range state.Grants {
		if grant.API != api || grant.ApprovedAt.IsZero() {
			continue
		}
		if grant.Scope == "global" {
			cfg.AllRepositories, cfg.ConsentedAt = true, grant.ApprovedAt
			break
		}
		for _, allowed := range grant.Agents {
			if allowed == agent && grant.Repo != "" {
				cfg.Directories = append(cfg.Directories, grant.Repo)
				if grant.Repo == repo {
					cfg.ConsentedAt = grant.ApprovedAt
				}
				break
			}
		}
	}
	if cfg.ConsentedAt.IsZero() {
		return cfg, fmt.Errorf("upload approval is required for this service; run the installer’s onboard command")
	}
	return cfg, nil
}

func agentHookEnv(config string) []string {
	var env []string
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "DOOMER_CONFIG_DIR=") {
			env = append(env, value)
		}
	}
	return append(env, "DOOMER_CONFIG_DIR="+config)
}

// Match the installer's directory lock so uninstall cannot remove private state
// between approval validation and detached watcher startup.
func agentInstallationLock(ctx context.Context, home string) (func(), error) {
	path := filepath.Join(home, "install.lock")
	for {
		if err := os.Mkdir(path, 0700); err == nil {
			return func() { _ = os.Remove(path) }, nil
		} else if !os.IsExist(err) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("installer is busy")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func newAgentWatchHookCommand() *cobra.Command {
	var home, agent, owner string
	c := &cobra.Command{Use: "agent-hook", Short: "Handle installed agent hooks directly in Go", Args: cobra.NoArgs}
	c.RunE = func(c *cobra.Command, _ []string) error {
		err := runAgentWatchHook(c, home, agent)
		if err != nil {
			// Infrastructure failures are visible but never represented as a review.
			_ = json.NewEncoder(c.OutOrStdout()).Encode(map[string]string{"systemMessage": "Doomer Quick review was not completed: " + err.Error()})
		}
		return nil
	}
	c.Flags().StringVar(&home, "manager-home", "", "installer state directory")
	c.Flags().StringVar(&agent, "agent", "", "coding agent identity")
	c.Flags().StringVar(&owner, "owner", "", "installer ownership marker")
	return c
}

func runAgentWatchHook(c *cobra.Command, home, agent string) error {
	if !filepath.IsAbs(home) || (agent != "codex" && agent != "claude") {
		return fmt.Errorf("invalid installed hook configuration")
	}
	if _, err := os.Stat(filepath.Join(home, "uninstalling.json")); err == nil {
		fmt.Fprint(c.OutOrStdout(), "{}")
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(c.InOrStdin(), (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return fmt.Errorf("invalid hook input")
	}
	var input watching.HookInput
	if json.Unmarshal(raw, &input) != nil {
		return fmt.Errorf("invalid hook input")
	}
	if input.Session == "" || input.CWD == "" || (input.Event != "UserPromptSubmit" && input.Event != "Stop") {
		fmt.Fprint(c.OutOrStdout(), "{}")
		return nil
	}
	ctx, cancel := context.WithTimeout(c.Context(), 40*time.Second)
	defer cancel()
	unlock, err := agentInstallationLock(ctx, home)
	if err != nil {
		return err
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	var state agentHookState
	if err := watching.Read(filepath.Join(home, "state.json"), &state); err != nil {
		return fmt.Errorf("cannot read installer state")
	}
	explicit := ""
	if c.Flags().Changed("api-url") || c.InheritedFlags().Changed("api-url") {
		explicit, _ = c.Flags().GetString("api-url")
	}
	api, err := agentHookAPI(explicit, os.Getenv("DOOMER_API_URL"), state.API)
	if err != nil {
		return err
	}
	repo, _ := watching.HookRepository(c.Context(), input)
	cfg, err := agentHookConsent(state, api, agent, repo)
	if err != nil {
		return err
	}
	config := filepath.Join(home, "native-state")
	root := filepath.Join(config, "watch", watching.Key(api+"\x00"+cfg.Profile))
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := prepareAgentWatcher(ctx, exe, home, root, config, repo, state, &cfg); err != nil {
		return err
	}
	unlock()
	unlock = nil
	// Reuse the existing native hook protocol and bounded Stop wait.
	child := exec.CommandContext(ctx, exe, "--api-url", api, "--profile", cfg.Profile, "watch", "hook", "--state", root, "--agent", agent)
	child.Env, child.Stdin = agentHookEnv(config), bytes.NewReader(raw)
	var result bytes.Buffer
	child.Stdout, child.Stderr = &result, c.ErrOrStderr()
	if err := child.Run(); err != nil {
		return fmt.Errorf("native hook did not complete: %w", err)
	}
	_, err = c.OutOrStdout().Write(result.Bytes())
	return err
}

func prepareAgentWatcher(ctx context.Context, exe, home, root, config, repo string, state agentHookState, cfg *watching.Config) error {
	lock, err := lifecycleLock(ctx, root)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := os.Stat(filepath.Join(home, "uninstalling.json")); err == nil {
		return fmt.Errorf("uninstall is in progress")
	}
	if paused(root) {
		return fmt.Errorf("watching is paused; resume it explicitly")
	}
	store := adversarylabs.ConfigStore{Path: filepath.Join(config, "config.json")}
	token := os.Getenv("DOOMER_TOKEN")
	if token == "" {
		token = state.Identities[cfg.API]
	}
	if token == "" {
		if saved, ok, err := store.ExactAuthE(adversarylabs.AuthKey(cfg.API, cfg.Profile)); err != nil {
			return err
		} else if ok {
			token = saved.Token
		}
	}
	if token == "" {
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return err
		}
		token = adversarylabs.AnonymousTokenPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
	}
	cfg.AccountKey = watching.Key(token)
	p, live, err := runningWatch(root)
	if err != nil {
		return err
	}
	if live {
		var previous watching.Config
		if err := watching.Read(filepath.Join(root, "config.json"), &previous); err != nil {
			return err
		}
		if !p.AllChanges || previous.AccountKey != cfg.AccountKey {
			return fmt.Errorf("restart the existing watcher to apply changed credentials or review mode")
		}
	}
	if err := store.SetAuth(adversarylabs.AuthKey(cfg.API, cfg.Profile), adversarylabs.Auth{Token: token}); err != nil {
		return err
	}
	if err := watching.Write(filepath.Join(root, "config.json"), cfg); err != nil {
		return err
	}
	if live || repo == "" {
		return nil
	}
	session := rand.Text()
	child := exec.Command(exe, "--api-url", cfg.API, "--profile", cfg.Profile, "watch", "--all-changes", "--managed-session", session)
	child.Dir, child.Env = repo, agentHookEnv(config)
	if err := startWatchProcess(child, filepath.Join(root, "watch.log")); err != nil {
		return err
	}
	return waitWatch(ctx, func() (bool, error) {
		p, live, err := runningWatch(root)
		return live && p.ManagedSession == session, err
	})
}
