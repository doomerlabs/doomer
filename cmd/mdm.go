package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/spf13/cobra"
)

const mdmExecutable = "/usr/local/libexec/doomer-mdm/doomer"
const mdmPreferences = "/Library/Managed Preferences/com.doomerlabs.doomer.mdm.plist"

func isMDMRuntimePath(path string) bool {
	return path == mdmExecutable || path == filepath.Join(mdmSystemDir, "doomer") ||
		path == filepath.Join(mdmSystemDir, "bootstrap-doomer")
}

var (
	mdmEnrollmentKey = regexp.MustCompile(`^doomer_mdm_[A-Za-z0-9_-]{43}$`)
	mdmToken         = regexp.MustCompile(`^adv_cli_[A-Za-z0-9_-]{43}$`)
	mdmUUID          = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	mdmEmail         = regexp.MustCompile(`^[^\s@]+@[^\s@]+$`)
)

type mdmProfile struct {
	API      string   `json:"ApiURL"`
	Key      string   `json:"EnrollmentKey"`
	Email    string   `json:"WorkEmail"`
	Agents   []string `json:"Agents"`
	Uploads  bool     `json:"AuthorizeReviewUploads"`
	Commands bool     `json:"AuthorizeReviewerCommands"`
}

type mdmAssignment struct {
	ID    string `json:"id"`
	API   string `json:"api"`
	Email string `json:"email"`
}

type mdmCredential struct {
	ClientID string   `json:"client_id"`
	API      string   `json:"api"`
	Email    string   `json:"email"`
	Agents   []string `json:"agents"`
}

func newMDMCommand() *cobra.Command {
	root := &cobra.Command{Use: "mdm", Short: "Run organization-managed macOS setup", Hidden: true,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		PersistentPostRun: func(*cobra.Command, []string) {},
	}
	var agent string
	hook := &cobra.Command{Use: "hook", Short: "Run a managed coding-agent hook", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return runMDM(c, agent, true)
	}}
	hook.Flags().StringVar(&agent, "agent", "", "coding agent")
	root.AddCommand(hook, &cobra.Command{Use: "enroll", Short: "Enroll the managed macOS user", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return runMDM(c, "", false)
	}})
	var configAgent, output string
	config := &cobra.Command{Use: "agent-config", Short: "Generate managed coding-agent settings", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return writeMDMAgentConfig(c.OutOrStdout(), configAgent, output)
	}}
	config.Flags().StringVar(&configAgent, "agent", "", "coding agent")
	config.Flags().StringVar(&output, "merge-codex", "", "merge into the system Codex hook file")
	root.AddCommand(config, newMDMUpdateCommand())
	return root
}

func parseMDMProfile(data []byte) (mdmProfile, error) {
	var p mdmProfile
	if len(data) > 16<<10 || json.Unmarshal(data, &p) != nil || !p.Uploads || !p.Commands ||
		!mdmEnrollmentKey.MatchString(p.Key) || len(p.Agents) == 0 {
		return p, fmt.Errorf("managed Doomer profile is incomplete")
	}
	p.Email = strings.ToLower(strings.TrimSpace(p.Email))
	p.API = strings.TrimRight(strings.TrimSpace(p.API), "/")
	if !mdmEmail.MatchString(p.Email) || p.API == "" || adversarylabs.ValidateAPIURL(p.API) != nil {
		return p, fmt.Errorf("managed Doomer profile is incomplete")
	}
	for _, agent := range p.Agents {
		if agent != "codex" && agent != "claude" {
			return p, fmt.Errorf("managed Doomer profile is incomplete")
		}
	}
	slices.Sort(p.Agents)
	p.Agents = slices.Compact(p.Agents)
	return p, nil
}

func runMDM(c *cobra.Command, agent string, hook bool) error {
	result, err := managedMDMOperation(c, agent, hook)
	if err != nil {
		// Only fixed messages leave the process. Profile and server response
		// contents may contain credentials or identifying information.
		message := "Managed Doomer setup unavailable."
		if errors.Is(err, errMDMDenied) {
			message = "Managed Doomer enrollment denied."
		}
		if runtime.GOOS == "darwin" {
			ctx, cancel := context.WithTimeout(c.Context(), time.Second)
			_ = exec.CommandContext(ctx, "/usr/bin/logger", "-t", "doomer-mdm", message).Run()
			cancel()
		}
		if !hook {
			return errors.New(message)
		}
		result = map[string]any{}
	}
	return json.NewEncoder(c.OutOrStdout()).Encode(result)
}

func managedMDMOperation(c *cobra.Command, agent string, hook bool) (any, error) {
	empty := map[string]any{}
	if runtime.GOOS != "darwin" || os.Geteuid() == 0 {
		return nil, fmt.Errorf("managed enrollment runs in a macOS user session")
	}
	var input []byte
	var event struct {
		Name string `json:"hook_event_name"`
	}
	if hook {
		var err error
		input, err = io.ReadAll(io.LimitReader(c.InOrStdin(), (1<<20)+1))
		if err != nil || len(input) > 1<<20 || json.Unmarshal(input, &event) != nil {
			return nil, fmt.Errorf("invalid managed hook input")
		}
	}
	ctx, cancel := context.WithTimeout(c.Context(), 5*time.Second)
	content, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", mdmPreferences).Output()
	cancel()
	if err != nil {
		return nil, err
	}
	profile, err := parseMDMProfile(content)
	if err != nil {
		return nil, err
	}
	if hook && !slices.Contains(profile.Agents, agent) {
		return empty, nil
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	home := filepath.Join(user, "Library", "Application Support", "Doomer MDM")
	statePath := filepath.Join(home, "state.json")
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	env := mdmCleanEnvironment(os.Environ())
	if !hook || event.Name == "UserPromptSubmit" {
		err = enrollMDM(c.Context(), profile, home, time.Now())
		if err != nil {
			return nil, err
		}
		if !hook && slices.Contains(profile.Agents, "codex") || agent == "codex" {
			lock, err := publock.Acquire(home, "codex-policy")
			if err != nil {
				return nil, err
			}
			err = applyMDMCodexPolicy(c.Context(), executable, home, profile.API, env)
			_ = lock.Close()
			if err != nil {
				return nil, err
			}
		}
	} else if _, err := os.Stat(statePath); errors.Is(err, os.ErrNotExist) {
		return empty, nil
	} else if err != nil {
		return nil, err
	}
	if !hook {
		return map[string]any{"status": "enrolled", "agents": profile.Agents}, nil
	}
	call := exec.CommandContext(c.Context(), executable, "--api-url", profile.API, "watch", "agent", "--agent", agent, "--manager-home", home)
	call.Env, call.Stdin = env, bytes.NewReader(input)
	out, err := call.Output()
	if err != nil || !json.Valid(out) {
		return nil, fmt.Errorf("managed review hook failed")
	}
	return json.RawMessage(out), nil
}

func mdmCleanEnvironment(environment []string) []string {
	return slices.DeleteFunc(environment, func(entry string) bool {
		key, _, _ := strings.Cut(entry, "=")
		return slices.Contains([]string{"DOOMER_TOKEN", "DOOMER_CONFIG_DIR", "DOOMER_PROFILE", "DOOMER_API_URL"}, key)
	})
}

var errMDMDenied = errors.New("managed Doomer enrollment denied")

func enrollMDM(ctx context.Context, p mdmProfile, home string, now time.Time) error {
	lock, err := publock.Acquire(home, "enrollment")
	if err != nil {
		return err
	}
	defer lock.Close()
	assignmentPath := filepath.Join(home, "installation.json")
	var assignment mdmAssignment
	if err := watching.Read(assignmentPath, &assignment); errors.Is(err, os.ErrNotExist) {
		uuid, err := newMDMUUID()
		if err != nil {
			return err
		}
		assignment = mdmAssignment{uuid, p.API, p.Email}
		if err := watching.Write(assignmentPath, assignment); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if !mdmUUID.MatchString(assignment.ID) || assignment.API != p.API || assignment.Email != p.Email {
		return fmt.Errorf("managed assignment changed")
	}

	credentials := filepath.Join(home, "credentials")
	file := filepath.Join(credentials, "config.json")
	key := adversarylabs.AuthKey(p.API, "default")
	statePath := filepath.Join(home, "state.json")
	var current struct {
		Managed mdmCredential `json:"managed"`
	}
	var saved adversarylabs.Config
	if watching.Read(statePath, &current) == nil && watching.Read(file, &saved) == nil {
		old := current.Managed
		auth := saved.Auths[key]
		expiry, _ := time.Parse(time.RFC3339Nano, auth.ExpiresAt)
		if old.API == p.API && old.Email == p.Email && slices.Equal(old.Agents, p.Agents) &&
			mdmUUID.MatchString(old.ClientID) && old.ClientID == auth.ClientID && mdmToken.MatchString(auth.Token) &&
			expiry.After(now.Add(10*time.Minute)) {
			return nil
		}
	}

	payload, err := json.Marshal(map[string]any{"email": p.Email, "installation_id": assignment.ID, "agents": p.Agents})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.API+"/v1/auth/mdm/enroll", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Key)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusForbidden {
		return errMDMDenied
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("managed enrollment service returned HTTP %d", response.StatusCode)
	}
	var auth adversarylabs.Auth
	decoder := json.NewDecoder(io.LimitReader(response.Body, 16<<10))
	if err := decoder.Decode(&auth); err != nil {
		return err
	}
	expiry, _ := time.Parse(time.RFC3339Nano, auth.ExpiresAt)
	if !mdmToken.MatchString(auth.Token) || !mdmUUID.MatchString(auth.ClientID) || !expiry.After(now) {
		return fmt.Errorf("invalid managed enrollment credential")
	}
	auth = adversarylabs.Auth{Token: auth.Token, ClientID: auth.ClientID, ExpiresAt: auth.ExpiresAt}
	if err := watching.Write(file, adversarylabs.Config{Auths: map[string]adversarylabs.Auth{key: auth}}); err != nil {
		return err
	}
	return watching.Write(statePath, map[string]any{
		"version": 1, "api": p.API,
		"managed":        mdmCredential{auth.ClientID, p.API, p.Email, p.Agents},
		"credentialDirs": map[string]string{p.API: credentials},
		"grants":         []agentUploadGrant{{Scope: "global", API: p.API, ApprovedAt: now.UTC()}},
	})
}

func applyMDMCodexPolicy(ctx context.Context, executable, home, api string, env []string) error {
	base := []string{"--api-url", api, "watch", "codex-policy", "--manager-home", home}
	call := func(args []string) (npmCodexPolicyReport, error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.Dir, cmd.Env = home, env
		out, err := cmd.Output()
		var result npmCodexPolicyReport
		if err == nil {
			err = json.Unmarshal(out, &result)
		}
		return result, err
	}
	report, err := call(base)
	if err != nil || len(report.Policies) != 1 || report.Policies[0].State == "declined" {
		return fmt.Errorf("managed Codex policy unavailable")
	}
	policy := report.Policies[0]
	if policy.State == "configured" {
		return nil
	}
	digest := npmCodexProposal(policy)
	if len(digest) != 64 {
		return fmt.Errorf("invalid managed Codex policy")
	}
	report, err = call(append(base, "--proposal", digest, "--decision", "allow"))
	if err != nil || len(report.Policies) != 1 || report.Policies[0].State != "configured" {
		return fmt.Errorf("could not apply managed Codex policy")
	}
	return nil
}

func writeMDMAgentConfig(out io.Writer, agent, merge string) error {
	if agent != "claude" && agent != "codex" || merge != "" && (agent != "codex" || merge != "/etc/codex/hooks.json") {
		return fmt.Errorf("invalid managed agent config request")
	}
	command := mdmExecutable + " mdm hook --agent " + agent
	config := npmJSONObject{}
	hooks := npmJSONObject{}
	if merge != "" {
		unlock, err := npmSetupLock(merge)
		if err != nil {
			return err
		}
		defer unlock()
		config, _, err = npmReadSettings(merge)
		if err != nil {
			return err
		}
		hooks, err = npmValidateHooks(config)
		if err != nil {
			return err
		}
	}
	for _, event := range npmHookEvents {
		if npmEventTimeout(agent, event) == 0 {
			continue
		}
		groups, _ := npmObjects(hooks[event])
		for i := 0; i < len(groups); {
			handlers, _ := npmObjects(groups[i]["hooks"])
			handlers = slices.DeleteFunc(handlers, func(h npmJSONObject) bool {
				return h["command"] == command || h["command"] == "/usr/local/libexec/doomer-mdm/node /usr/local/libexec/doomer-mdm/manager/bin/doomer.js mdm-hook --agent "+agent
			})
			if len(handlers) == 0 {
				groups = slices.Delete(groups, i, i+1)
			} else {
				groups[i]["hooks"] = handlers
				i++
			}
		}
		timeout := 45
		if event == "Stop" {
			timeout = 75
		}
		hooks[event] = append(groups, npmJSONObject{"hooks": []npmJSONObject{{"type": "command", "command": command, "timeout": timeout}}})
	}
	config["hooks"] = hooks
	if agent == "claude" {
		rules, explanation, err := npmClaudeRules([]string{mdmExecutable})
		if err != nil {
			return err
		}
		config["permissions"] = npmJSONObject{"allow": rules}
		config["autoMode"] = npmJSONObject{"allow": append([]string{"$defaults"}, explanation...)}
	}
	if merge != "" {
		data, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			return err
		}
		return writeNPMFile(merge, append(data, '\n'), 0644)
	}
	return json.NewEncoder(out).Encode(config)
}

func newMDMUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}
