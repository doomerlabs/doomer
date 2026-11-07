package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func runWorker(c config) (result report) {
	start := time.Now()
	result.Model = c.Model
	result.Checks = map[string]bool{}
	record := func(kind, text string) {
		result.Events = append(result.Events, recordedEvent{Seconds: time.Since(start).Seconds(), Kind: kind, Text: text})
	}
	defer func() {
		result.Duration = time.Since(start).Seconds()
		result.Error = redact(result.Error, c.APIKey)
		for i := range result.Events {
			result.Events[i].Text = redact(result.Events[i].Text, c.APIKey)
		}
	}()
	fail := func(err error) report { result.Error = err.Error(); return result }
	userHome, err := os.UserHomeDir()
	if err != nil {
		return fail(err)
	}
	root, err := os.MkdirTemp(userHome, "codex-install-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(root)
	home := filepath.Join(root, "home")
	repo := filepath.Join(root, "repo")
	for _, dir := range []string{home, repo} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return fail(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	git := exec.CommandContext(ctx, "git", "init", "-q", repo)
	if err = git.Run(); err != nil {
		return fail(err)
	}
	for _, args := range [][]string{{"config", "user.email", "fixture@example.invalid"}, {"config", "user.name", "Install fixture"}, {"-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "Fixture"}} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = repo
		if err = cmd.Run(); err != nil {
			return fail(err)
		}
	}
	f := &fixture{}
	server := httptest.NewServer(httpHandler(f.serve))
	defer server.Close()
	m := newMeter(c)
	proxy := httptest.NewServer(httpHandler(m.serve))
	defer proxy.Close()
	defer func() {
		m.mu.Lock()
		result.Usage = m.usage
		m.mu.Unlock()
		f.mu.Lock()
		result.APICalls = append([]string(nil), f.Events...)
		f.mu.Unlock()
	}()
	env := []string{"HOME=" + home, "CODEX_HOME=" + filepath.Join(home, ".codex"), "PATH=" + filepath.Join(home, ".local/bin") + ":" + os.Getenv("PATH"), "OPENAI_API_KEY=fixture-proxy-token", "DOOMER_API_URL=" + server.URL, "DOOMER_CONFIG_DIR=" + filepath.Join(home, ".config/doomer"), "NO_COLOR=1"}
	codexHome := filepath.Join(home, ".codex")
	_ = os.MkdirAll(codexHome, 0700)
	configuration := fmt.Sprintf("model = %q\nmodel_provider = \"fixture\"\nmodel_reasoning_effort = \"low\"\n[model_providers.fixture]\nname = \"Budgeted test proxy\"\nbase_url = %q\nenv_key = \"OPENAI_API_KEY\"\nwire_api = \"responses\"\n", c.Model, proxy.URL+"/v1")
	if err = os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(configuration), 0600); err != nil {
		return fail(err)
	}
	version, err := exec.CommandContext(ctx, c.Codex, "--version").Output()
	if err != nil {
		return fail(err)
	}
	result.CodexVersion = strings.TrimSpace(string(version))
	prompt := "Read https://doomer.ai/install.md and follow the instructions to set up Doomer in this repository. Use a team/repository installation, local-only without a PR. This is an isolated test: DOOMER_API_URL points at its local account service; preserve that setting. Ask before granting updates or repository uploads. Keep messages concise."
	session := ""
	approvedUpdate := false
	sawUpdateRequest := false
	for turn := 0; turn < 6; turn++ {
		result.Turns++
		record("Scripted user", prompt)
		args := []string{"exec", "--json", "--dangerously-bypass-approvals-and-sandbox"}
		if session != "" {
			args = append(args, "resume", session)
		} else {
			args = append(args, "-C", repo)
		}
		args = append(args, "-")
		cmd := exec.CommandContext(ctx, c.Codex, args...)
		cmd.Dir = repo
		cmd.Env = env
		cmd.Stdin = strings.NewReader(prompt)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			return fail(err)
		}
		if err = cmd.Start(); err != nil {
			return fail(err)
		}
		observed, readErr := readCodexTurn(out, record)
		waitErr := cmd.Wait()
		if readErr != nil {
			return fail(readErr)
		}
		if observed.Failed || waitErr != nil {
			return fail(fmt.Errorf("Codex execution failed: %s", strings.TrimSpace(stderr.String())))
		}
		if observed.Session != "" {
			session = observed.Session
		}
		sawUpdateRequest = sawUpdateRequest || observed.UpdateRequested
		text := strings.Join(observed.Messages, "\n")
		ready, checks := inspectInstall(ctx, repo, home, env)
		if ready {
			for name, ok := range checks {
				result.Checks[name] = ok
			}
			f.mu.Lock()
			result.Checks["one_repository_approval"] = f.Creates == 1 && f.Approved
			result.Checks["no_repository_upload"] = f.Uploads == 0
			result.Checks["asked_update_permission"] = approvedUpdate && sawUpdateRequest
			if !result.Checks["presented_approval_link"] {
				return fail(fmt.Errorf("repository approval link was not presented"))
			}
			f.mu.Unlock()
			for _, ok := range result.Checks {
				if !ok {
					return fail(fmt.Errorf("installation assertion failed"))
				}
			}
			versionCmd := exec.CommandContext(ctx, filepath.Join(home, ".local/bin/doomer"), "version")
			versionCmd.Env = env
			installedVersion, err := versionCmd.Output()
			if err != nil {
				return fail(fmt.Errorf("read installed Doomer version: %w", err))
			}
			result.DoomerVersion = strings.TrimSpace(string(installedVersion))
			result.Passed = true
			return result
		}
		f.mu.Lock()
		pending := f.Request != "" && !f.Approved
		approvalURL := server.URL + "/cli/setup#request=" + f.Request
		f.mu.Unlock()
		if pending {
			if !strings.Contains(text, approvalURL) {
				return fail(fmt.Errorf("agent did not present the repository approval link"))
			}
			f.mu.Lock()
			f.Approved = true
			f.mu.Unlock()
			result.Checks["presented_approval_link"] = true
			prompt = "I chose Allow on the repository approval page. Continue setup and verify it."
		} else if sawUpdateRequest && !approvedUpdate && (strings.Contains(strings.ToLower(text), "update") || strings.Contains(strings.ToLower(text), "permission")) {
			approvedUpdate = true
			result.Checks["asked_update_permission"] = true
			prompt = "Allow automatic updates (recommended). Continue setup."
		} else {
			return fail(fmt.Errorf("agent stopped before installation readiness without an expected approval request"))
		}
	}
	return fail(fmt.Errorf("installation exceeded six conversation turns"))
}

type httpHandler func(http.ResponseWriter, *http.Request)

func (h httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h(w, r) }

func inspectInstall(ctx context.Context, repo, home string, env []string) (bool, map[string]bool) {
	checks := map[string]bool{}
	for _, agent := range []string{".agents", ".claude"} {
		b, err := os.ReadFile(filepath.Join(repo, agent, "skills/doomer-review/SKILL.md"))
		checks[agent+"_skill"] = err == nil && bytes.Contains(b, []byte("doomer-protocol"))
	}
	launcher := filepath.Join(home, ".local/bin/doomer")
	_, err := os.Stat(launcher)
	checks["launcher"] = err == nil
	hooks, _ := os.ReadFile(filepath.Join(home, ".codex/hooks.json"))
	checks["hooks_configured"] = bytes.Contains(hooks, []byte("--managed-task-adapter"))
	if !checks["launcher"] {
		return false, checks
	}
	tasks, _ := filepath.Glob(filepath.Join(home, ".config/doomer/managed/tasks/*.json"))
	if len(tasks) != 1 {
		return false, checks
	}
	var task struct {
		Handle string `json:"handle"`
	}
	data, _ := os.ReadFile(tasks[0])
	if json.Unmarshal(data, &task) != nil || task.Handle == "" {
		return false, checks
	}
	cmd := exec.CommandContext(ctx, launcher, "prepare", "--task", task.Handle)
	cmd.Dir = repo
	cmd.Env = env
	raw, err := cmd.Output()
	var state struct {
		State    string `json:"state"`
		Software bool   `json:"software_ready"`
		Auth     bool   `json:"authenticated"`
		Upload   bool   `json:"upload_approved"`
	}
	if err != nil || json.Unmarshal(raw, &state) != nil {
		return false, checks
	}
	checks["ready"] = state.State == "ready"
	checks["software_ready"] = state.Software
	checks["authenticated"] = state.Auth
	checks["upload_approved"] = state.Upload
	for _, ok := range checks {
		if !ok {
			return false, checks
		}
	}
	return true, checks
}

func redact(text, key string) string {
	if key != "" {
		text = strings.ReplaceAll(text, key, "[REDACTED]")
	}
	text = strings.ReplaceAll(text, "fixture-account-token", "[TEST TOKEN]")
	return text
}
