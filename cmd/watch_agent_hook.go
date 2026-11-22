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
	"slices"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/doctor"
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
	Managed *struct {
		Agents []string `json:"agents"`
	} `json:"managed"`
	CodexPolicies []struct {
		ConfigPath  string `json:"configPath"`
		ExtraPolicy string `json:"extraPolicy"`
	} `json:"codexPolicies"`
	Version        int                `json:"version"`
	API            string             `json:"api"`
	Identities     map[string]string  `json:"identities"`
	CredentialDirs map[string]string  `json:"credentialDirs"`
	Grants         []agentUploadGrant `json:"grants"`
}

type agentUploadGrant struct {
	Scope      string    `json:"scope"`
	API        string    `json:"api"`
	Repo       string    `json:"repo,omitempty"`
	Agents     []string  `json:"agents,omitempty"`
	ApprovedAt time.Time `json:"approvedAt"`
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
	if state.Managed != nil && (state.API != api || !slices.Contains(state.Managed.Agents, agent)) {
		return watching.Config{}, fmt.Errorf("agent or service is outside managed Doomer policy")
	}
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
	c := &cobra.Command{Use: "agent", Aliases: []string{"agent-hook"}, Short: "Start and stop watch for an agent", Args: cobra.NoArgs}
	// Hook execution must not load or create legacy watcher state as a side effect.
	c.PersistentPreRunE = func(*cobra.Command, []string) error { return nil }
	c.PersistentPostRun = func(*cobra.Command, []string) {}
	c.RunE = func(c *cobra.Command, _ []string) error {
		result, err := runAgentWatchHook(c, home, agent)
		if err != nil {
			var state agentHookState
			if watching.Read(filepath.Join(home, "state.json"), &state) == nil && state.Managed != nil {
				result = map[string]any{"managedDoomerError": err.Error()}
			} else {
				result = map[string]any{"systemMessage": "Doomer Quick review was not completed: " + err.Error()}
			}
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
	if input.Session == "" {
		return empty, nil
	}
	// Codex also runs composer suggestions as background tasks in the same
	// working directory. They must not observe edits made by the real task.
	if agent == "codex" && input.Event == "UserPromptSubmit" && strings.HasPrefix(strings.TrimSpace(input.Prompt), "This is an app-generated request for a suggested next user message. The user did not write this message.") {
		return empty, nil
	}
	if input.Event == "UserPromptSubmit" && doctor.IsSessionPrompt(input.Prompt) {
		return runAgentSessionProbe(c, home, agent, input.CWD), nil
	}
	turn := input.Turn
	if turn == "" {
		turn = input.PromptID
	}
	id := hookIdentity{Namespace: home, Agent: agent, Session: input.Session, Turn: turn}
	switch input.Event {
	case "PermissionDenied":
		if agent == "claude" && input.Tool == "Bash" {
			reportClaudeReviewDenial(c.Context(), id.Key(), input)
		}
		return empty, nil
	case "Stop", "Interrupt", "SessionEnd":
		operation := "finish"
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
		if operation == "finish" {
			// The host supplies the actual final assistant message. A tool result
			// or a model's assertion that it displayed a notice is not a receipt.
			settled := !result.ReviewPending && len(result.HypothesisRequests) == 0
			for _, finding := range result.Findings {
				settled = settled && finding.Disposition != nil
			}
			acknowledged := false
			if settled {
				for _, notice := range result.Notices {
					if notice.NotifyUser && !notice.Blocking && noticeAppearsInReply(notice, input.Message) {
						result, err = livewatch.RecordNoticeInReply(ctx, id.Key(), notice.ID, notice.Revision)
						if err != nil {
							return empty, err
						}
						acknowledged = true
					}
				}
			}
			if acknowledged {
				result, err = livewatch.Call(ctx, id.Key(), "finish")
				if err != nil {
					return empty, err
				}
			}
			if agent == "claude" {
				if result.RepeatedStopHandoff {
					if _, err := livewatch.Call(ctx, id.Key(), "cancel"); err != nil {
						return empty, err
					}
				}
				return claudeStopFeedback(result, id.Key(), input.StopActive), nil
			}
			report := makeFeedbackReport(result)
			feedback := codexStopFeedback(result, input.StopActive)
			unansweredWithoutContinuation := report.Unanswered > 0 && feedback["decision"] != "block"
			if result.RepeatedStopHandoff && len(result.HypothesisRequests) > 0 && len(feedback) == 0 {
				if _, err := livewatch.Call(ctx, id.Key(), "cancel-assessment"); err != nil {
					return empty, err
				}
			} else if unansweredWithoutContinuation || (input.StopActive && (!report.Complete || report.NoticesPending) && len(result.HypothesisRequests) == 0) {
				// End the normal lifecycle without inventing feedback or retrying a denied action.
				if _, err := livewatch.Call(ctx, id.Key(), "cancel"); err != nil {
					return empty, err
				}
			}
			return feedback, nil
		}
		return empty, nil
	case "UserPromptSubmit":
		if input.CWD == "" {
			return empty, nil
		}
	default:
		return empty, nil
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
			if existing.Directory != directory {
				return empty, fmt.Errorf("this turn already has a watcher with different configuration")
			}
			return watchAgentContextForAgent(id.Key(), agent, directory), nil
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
	cfg := watchConfiguration{Config: livewatch.Config{RequireHypotheses: true, ID: rand.Text(), ControlKey: id.Key(), Directory: directory, Agent: agent, Request: &adversarylabs.WorkspaceUserRequest{Prompt: string(prompt), SessionID: input.Session, TurnID: requestTurn}}, ManagerHome: home}
	data, err := json.Marshal(cfg)
	if err != nil {
		return empty, err
	}
	exe, err := turnExecutable()
	if err != nil {
		return empty, err
	}
	child := exec.Command(exe, agentWatchArguments(c)...)
	exited, err := startBackgroundWatch(child, data)
	if err != nil {
		return empty, err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if result, live := livewatch.Available(ctx, id.Key()); live {
			if result.Directory != directory {
				return empty, fmt.Errorf("watcher identity collision")
			}
			return watchAgentContextForAgent(id.Key(), agent, directory), nil
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
	var findings, failures, links []string
	completed := 0
	for _, review := range result.Results {
		if review.URL != "" {
			links = append(links, "[Doomer review]("+review.URL+")")
		}
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
	linkText := ""
	if len(links) > 0 {
		linkText = "\n" + strings.Join(links, "\n")
	}
	if len(findings) > 0 {
		return map[string]any{"decision": "block", "reason": "Doomer Quick review findings:\n" + strings.Join(findings, "\n\n") + linkText}
	}
	if len(failures) > 0 {
		return map[string]any{"systemMessage": "Doomer Quick review was not completed:\n" + strings.Join(failures, "\n") + linkText}
	}
	if completed > 0 {
		return map[string]any{"systemMessage": fmt.Sprintf("Doomer Quick review completed for %d repositories; no findings returned.", completed) + linkText}
	}
	return map[string]any{}
}
