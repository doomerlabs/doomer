package cmd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/livewatch"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"github.com/spf13/cobra"
)

var turnExecutable = os.Executable

// Session/turn identity belongs to the hook, never to the watcher.
type hookIdentity struct{ Namespace, Agent, Session, Turn string }

func (id hookIdentity) Key() string {
	return livewatch.Key(id.Namespace + "\x00" + id.Agent + "\x00" + id.Session + "\x00" + id.Turn)
}

// The manager owns installation and approval. Hooks read that data directly;
// neither starting a watcher nor delivering a result requires Node at runtime.
type agentHookState struct {
	Version        int               `json:"version"`
	API            string            `json:"api"`
	Identities     map[string]string `json:"identities"`
	CredentialDirs map[string]string `json:"credentialDirs"`
	Grants         []struct {
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
	c := &cobra.Command{Use: "agent-hook", Short: "Start and stop watch for an agent", Args: cobra.NoArgs}
	// Hook execution must not load or create legacy watcher state as a side effect.
	c.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	c.RunE = func(c *cobra.Command, _ []string) error {
		result, err := runAgentWatchHook(c, home, agent)
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

func runAgentWatchHook(c *cobra.Command, home, agent string) (map[string]any, error) {
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
	id := hookIdentity{Namespace: home, Agent: agent, Session: input.Session, Turn: turn}
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
		if _, live := livewatch.Available(ctx, id.Key()); !live {
			return empty, nil
		}
		result, err := livewatch.Call(ctx, id.Key(), operation)
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
	token, err := watchToken(c, api, state)
	if err != nil {
		return empty, err
	}
	directory, err := filepath.EvalSymlinks(input.CWD)
	if err != nil {
		return empty, err
	}
	ctx, cancel := context.WithTimeout(c.Context(), 25*time.Second)
	defer cancel()
	if existing, live := livewatch.Available(ctx, id.Key()); live {
		if turn == "" {
			if _, err := livewatch.Call(ctx, id.Key(), "cancel"); err != nil {
				return empty, err
			}
		} else {
			if existing.API != api || existing.Directory != directory {
				return empty, fmt.Errorf("this turn already has a watcher with different configuration")
			}
			return empty, nil
		}
	}
	requestTurn := turn
	if requestTurn == "" {
		requestTurn = rand.Text()
	}
	prompt := []rune(input.Prompt)
	if len(prompt) > 4000 {
		prompt = prompt[:4000]
	}
	cfg := watchConfiguration{Config: livewatch.Config{ID: rand.Text(), ControlKey: id.Key(), Directory: directory, API: api, Token: token, Agent: agent, Request: &adversarylabs.WorkspaceUserRequest{Prompt: string(prompt), SessionID: input.Session, TurnID: requestTurn}}, ManagerHome: home}
	data, err := json.Marshal(cfg)
	if err != nil {
		return empty, err
	}
	exe, err := turnExecutable()
	if err != nil {
		return empty, err
	}
	child := exec.Command(exe, "watch", "--config-stdin")
	exited, err := startBackgroundWatch(child, data)
	if err != nil {
		return empty, err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if result, live := livewatch.Available(ctx, id.Key()); live {
			if result.API != api || result.Directory != directory {
				return empty, fmt.Errorf("watcher identity collision")
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
			return empty, fmt.Errorf("watcher exited before startup: %v", err)
		case <-ctx.Done():
			_ = child.Process.Kill()
			return empty, fmt.Errorf("watcher did not become ready")
		case <-ticker.C:
		}
	}
}

func turnHookResult(result livewatch.Response) map[string]any {
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
