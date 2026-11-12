package cmd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/turnwatch"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"github.com/spf13/cobra"
)

var turnExecutable = os.Executable

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
				if grant.Repo == repo || repo == "" {
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

func newAgentWatchHookCommand() *cobra.Command {
	var home, agent, owner string
	c := &cobra.Command{Use: "agent-hook", Short: "Handle installed agent hooks with an in-memory turn worker", Args: cobra.NoArgs}
	// Hook execution must not load or create legacy watcher state as a side effect.
	c.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	c.RunE = func(c *cobra.Command, _ []string) error {
		result, err := runTurnHook(c, home, agent)
		if err != nil {
			result = map[string]any{"systemMessage": "Doomer Quick review was not completed: " + err.Error()}
		}
		return json.NewEncoder(c.OutOrStdout()).Encode(result)
	}
	c.Flags().StringVar(&home, "manager-home", "", "installer state directory")
	c.Flags().StringVar(&agent, "agent", "", "coding agent identity")
	c.Flags().StringVar(&owner, "owner", "", "installer ownership marker")
	return c
}

func turnToken(api string, state agentHookState) (string, error) {
	if token := os.Getenv("DOOMER_TOKEN"); token != "" {
		return token, nil
	}
	if token := state.Identities[api]; token != "" {
		return token, nil
	}
	return "", fmt.Errorf("credentials are missing; run onboard")
}

func runTurnHook(c *cobra.Command, home, agent string) (map[string]any, error) {
	empty := map[string]any{}
	if !filepath.IsAbs(home) || (agent != "codex" && agent != "claude") {
		return empty, fmt.Errorf("invalid installed hook configuration")
	}
	raw, err := io.ReadAll(io.LimitReader(c.InOrStdin(), (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return empty, fmt.Errorf("invalid hook input")
	}
	var input watching.HookInput
	if json.Unmarshal(raw, &input) != nil {
		return empty, fmt.Errorf("invalid hook input")
	}
	if input.Session == "" || input.CWD == "" {
		return empty, nil
	}
	turn := input.Turn
	if turn == "" {
		turn = input.PromptID
	}
	id := turnwatch.Identity{Namespace: home, Agent: agent, Session: input.Session, Turn: turn}
	switch input.Event {
	case "Stop", "Interrupt", "SessionEnd":
		operation := "stop"
		wait := 55 * time.Second
		if input.Event != "Stop" {
			operation = "cancel"
			wait = 10 * time.Second
		}
		ctx, cancel := context.WithTimeout(c.Context(), wait)
		defer cancel()
		if _, live := turnwatch.Available(ctx, id); !live {
			return empty, nil
		}
		result, err := turnwatch.Call(ctx, id, operation)
		if err != nil {
			return empty, err
		}
		return turnHookResult(result), nil
	case "UserPromptSubmit":
	default:
		return empty, nil
	}
	var state agentHookState
	if err := watching.Read(filepath.Join(home, "state.json"), &state); err != nil {
		return empty, fmt.Errorf("cannot read installer approval")
	}
	explicit := ""
	if c.Flags().Changed("api-url") || c.InheritedFlags().Changed("api-url") {
		explicit, _ = c.Flags().GetString("api-url")
	}
	api, err := agentHookAPI(explicit, os.Getenv("DOOMER_API_URL"), state.API)
	if err != nil {
		return empty, err
	}
	if _, err := agentHookConsent(state, api, agent, ""); err != nil {
		return empty, err
	}
	token, err := turnToken(api, state)
	if err != nil {
		return empty, err
	}
	directory, err := filepath.EvalSymlinks(input.CWD)
	if err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(c.Context(), 25*time.Second)
	defer cancel()
	if existing, live := turnwatch.Available(ctx, id); live {
		if turn == "" {
			if _, err := turnwatch.Call(ctx, id, "cancel"); err != nil {
				return empty, err
			}
		} else {
			if existing.API != api || existing.Directory != directory {
				return empty, fmt.Errorf("this turn already has a worker with different configuration")
			}
			return empty, nil
		}
	}
	requestTurn := turn
	if requestTurn == "" {
		requestTurn = rand.Text()
	}
	cfg := turnwatch.Config{Identity: id, Directory: directory, API: api, Token: token, Prompt: input.Prompt, RequestTurn: requestTurn, ManagerHome: home}
	data, err := json.Marshal(cfg)
	if err != nil {
		return empty, err
	}
	exe, err := turnExecutable()
	if err != nil {
		return empty, err
	}
	child := exec.Command(exe, "watch", "turn-worker")
	exited, err := startTurnProcess(child, data)
	if err != nil {
		return empty, err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if result, live := turnwatch.Available(ctx, id); live {
			if result.API != api || result.Directory != directory {
				return empty, fmt.Errorf("worker identity collision")
			}
			return empty, nil
		}
		select {
		case err := <-exited:
			if err != nil && strings.Contains(err.Error(), "address already in use") {
				// A duplicate delivery raced this hook. The kernel chose one
				// worker; wait for that worker's ready handshake instead.
				exited = nil
				continue
			}
			return empty, fmt.Errorf("turn worker exited before startup: %v", err)
		case <-ctx.Done():
			_ = child.Process.Kill()
			return empty, fmt.Errorf("turn worker did not become ready")
		case <-ticker.C:
		}
	}
}

func turnHookResult(result turnwatch.Response) map[string]any {
	var findings, failures []string
	completed := 0
	for _, review := range result.Results {
		if review.Status == "ready" {
			completed++
		}
		if review.Error != "" {
			failures = append(failures, filepath.Base(review.Repository)+": "+review.Error)
		}
		for _, lead := range review.Leads {
			if !sourcesnapshot.ValidPath(lead.Path) || lead.Line < 1 {
				continue
			}
			findings = append(findings, fmt.Sprintf("%s — %s:%d: %s\nCheck: %s", filepath.Base(review.Repository), lead.Path, lead.Line, lead.Concern, lead.Check))
		}
	}
	if len(findings) > 0 {
		return map[string]any{"decision": "block", "reason": "Doomer Quick review findings:\n" + strings.Join(findings, "\n\n")}
	}
	if len(failures) > 0 {
		return map[string]any{"systemMessage": "Doomer Quick review was not completed:\n" + strings.Join(failures, "\n")}
	}
	if completed > 0 {
		return map[string]any{"systemMessage": fmt.Sprintf("Doomer Quick review completed for %d repositories; no findings returned.", completed)}
	}
	return map[string]any{}
}

func newTurnWorkerCommand() *cobra.Command {
	c := &cobra.Command{Use: "turn-worker", Hidden: true, Args: cobra.NoArgs}
	c.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	c.RunE = func(c *cobra.Command, _ []string) error {
		var cfg turnwatch.Config
		if err := json.NewDecoder(io.LimitReader(c.InOrStdin(), 1<<20)).Decode(&cfg); err != nil {
			return err
		}
		if !filepath.IsAbs(cfg.Directory) || !filepath.IsAbs(cfg.ManagerHome) || cfg.Identity.Namespace != cfg.ManagerHome || cfg.Identity.Session == "" || (cfg.Identity.Agent != "codex" && cfg.Identity.Agent != "claude") || cfg.Token == "" || cfg.RequestTurn == "" {
			return errors.New("invalid turn worker configuration")
		}
		if err := adversarylabs.ValidateAPIURL(cfg.API); err != nil {
			return err
		}
		allowed := func(ctx context.Context, path string) bool {
			var state agentHookState
			if _, err := os.Stat(filepath.Join(cfg.ManagerHome, "uninstalling.json")); err == nil {
				return false
			}
			if watching.Read(filepath.Join(cfg.ManagerHome, "state.json"), &state) != nil {
				return false
			}
			consent, err := agentHookConsent(state, cfg.API, cfg.Identity.Agent, path)
			return err == nil && (path == "" || watching.Allowed(consent, path))
		}
		client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, cfg.API)
		return turnwatch.Serve(c.Context(), cfg, client, allowed)
	}
	return c
}
