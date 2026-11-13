package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/livewatch"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

// Exercise subprocesses with the test executable, without building or replacing
// the developer's CLI. No Node runtime is involved.
func TestAgentHookProcess(t *testing.T) {
	if os.Getenv("DOOMER_AGENT_HOOK_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			command := NewRootCommand()
			command.SetArgs(os.Args[i+1:])
			if err := command.Execute(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestAgentHookWorkersArePerTurnAndLeaveNoRuntimeFiles(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("local worker IPC requires Linux or macOS")
	}
	home, workspace := t.TempDir(), t.TempDir()
	api := "http://localhost:1/api"
	state := `{"version":1,"api":"` + api + `","identities":{"` + api + `":"test"},"grants":[{"scope":"global","api":"` + api + `","approvedAt":"2026-11-12T12:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(home, "state.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "test-cli")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+watching.Quote(executable)+" -test.run=^TestAgentHookProcess$ -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	previous := turnExecutable
	turnExecutable = func() (string, error) { return wrapper, nil }
	t.Cleanup(func() { turnExecutable = previous })
	t.Setenv("DOOMER_AGENT_HOOK_TEST_PROCESS", "1")
	t.Setenv("DOOMER_TOKEN", "")
	t.Setenv("DOOMER_CONFIG_DIR", t.TempDir())
	t.Setenv("DOOMER_API_URL", api)
	call := func(event, agent, turn string) {
		t.Helper()
		cwd := workspace
		if event != "UserPromptSubmit" {
			cwd = ""
		}
		input, _ := json.Marshal(watching.HookInput{Event: event, Session: "session", Turn: turn, CWD: cwd, Prompt: "test"})
		c := NewRootCommand()
		c.SetArgs([]string{"watch", "agent-hook", "--manager-home", home, "--agent", agent})
		c.SetIn(bytes.NewReader(input))
		var output bytes.Buffer
		c.SetOut(&output)
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), "not completed") {
			t.Fatal(output.String())
		}
		if event == "UserPromptSubmit" {
			var result struct {
				Specific struct {
					Event   string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			wantContext := "watch instructions --control"
			if agent == "claude" {
				wantContext = "Stop hook will deliver the review report"
			}
			if json.Unmarshal(output.Bytes(), &result) != nil || result.Specific.Event != "UserPromptSubmit" || !strings.Contains(result.Specific.Context, wantContext) {
				t.Fatalf("startup did not supply model context: %s", output.String())
			}
		}
	}
	ids := []hookIdentity{{Namespace: home, Agent: "codex", Session: "session", Turn: "one"}, {Namespace: home, Agent: "codex", Session: "session", Turn: "two"}, {Namespace: home, Agent: "claude", Session: "session", Turn: "one"}}
	t.Cleanup(func() {
		for _, id := range ids {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, _ = livewatch.Call(ctx, id.Key(), "cancel")
			cancel()
		}
	})
	for _, id := range ids {
		call("UserPromptSubmit", id.Agent, id.Turn)
	}
	call("UserPromptSubmit", "codex", "one") // Duplicate delivery reuses this turn only.
	call("Stop", "codex", "one")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, live := livewatch.Available(ctx, ids[0].Key()); live {
		t.Fatal("ended turn still has a worker")
	}
	for _, id := range ids[1:] {
		if _, live := livewatch.Available(ctx, id.Key()); !live {
			t.Fatal("ending one turn stopped another worker")
		}
	}
	call("Interrupt", "codex", "two")
	call("SessionEnd", "claude", "one")
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 1 {
		t.Fatalf("runtime polluted manager home: %v %v", entries, err)
	}
	after, _ := os.ReadFile(filepath.Join(home, "state.json"))
	if string(after) != state {
		t.Fatal("worker changed durable approval")
	}
}

func TestAgentHookKilledWorkerCanRestartWithoutCleanup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux abstract socket crash cleanup")
	}
	home, workspace := t.TempDir(), t.TempDir()
	api := "http://localhost:1/api"
	if err := os.WriteFile(filepath.Join(home, "state.json"), []byte(`{"version":1,"api":"`+api+`","identities":{"`+api+`":"test"},"grants":[{"scope":"global","api":"`+api+`","approvedAt":"2026-11-12T12:00:00Z"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "test-cli")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+watching.Quote(executable)+" -test.run=^TestAgentHookProcess$ -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOOMER_AGENT_HOOK_TEST_PROCESS", "1")
	id := hookIdentity{Namespace: home, Agent: "codex", Session: "session", Turn: "turn"}
	data, _ := json.Marshal(watchConfiguration{Config: livewatch.Config{ID: "watch", ControlKey: id.Key(), Directory: workspace, API: api, Token: "test"}, ManagerHome: home})
	start := func() (*exec.Cmd, <-chan error) {
		t.Helper()
		cmd := exec.Command(wrapper, "watch", "--config-stdin")
		done, err := startBackgroundWatch(cmd, data)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		for {
			if _, live := livewatch.Available(ctx, id.Key()); live {
				return cmd, done
			}
			select {
			case <-ctx.Done():
				t.Fatal("worker startup timed out")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	first, done := start()
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done
	_, replacement := start()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := livewatch.Call(ctx, id.Key(), "cancel"); err != nil {
		t.Fatal(err)
	}
	if err := <-replacement; err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 1 {
		t.Fatalf("crash left runtime files: %v %v", entries, err)
	}
}

func TestAgentHookEndpointPrecedence(t *testing.T) {
	for _, tc := range []struct{ flag, env, saved, want string }{
		{"https://flag.test/api", "http://localhost:3000/api", "https://saved.test/api", "https://flag.test/api"},
		{"", "http://localhost:3000/api", "https://saved.test/api", "http://localhost:3000/api"},
		{"", "", "https://saved.test/api", "https://saved.test/api"},
		{"", "", "", adversarylabs.DefaultAPIURL},
	} {
		got, err := agentHookAPI(tc.flag, tc.env, tc.saved)
		if err != nil || got != tc.want {
			t.Fatalf("endpoint = %q, %v; want %q", got, err, tc.want)
		}
	}
	if _, err := agentHookAPI("", "http://public.example/api", ""); err == nil {
		t.Fatal("accepted insecure public endpoint")
	}
}

func TestAgentHookApprovalIsolation(t *testing.T) {
	var state agentHookState
	if err := json.Unmarshal([]byte(`{"grants":[{"scope":"global","api":"http://localhost:3000/api","approvedAt":"2026-11-12T12:00:00Z"},{"repo":"/work/project","agents":["codex"],"api":"https://doomer.ai/api","approvedAt":"2026-11-12T12:00:00Z"}]}`), &state); err != nil {
		t.Fatal(err)
	}
	cfg, err := agentHookConsent(state, "http://localhost:3000/api", "claude", "/anywhere")
	if err != nil || !cfg.AllRepositories || !cfg.AllChanges {
		t.Fatalf("global approval: %+v %v", cfg, err)
	}
	for _, tc := range []struct{ agent, repo string }{{"codex", "/other"}, {"claude", "/work/project"}} {
		if _, err := agentHookConsent(state, "https://doomer.ai/api", tc.agent, tc.repo); err == nil {
			t.Fatal("broadened scoped approval")
		}
	}
	cfg, err = agentHookConsent(state, "https://doomer.ai/api", "codex", "/work/project")
	if err != nil || cfg.AllRepositories {
		t.Fatal("lost repository scope")
	}
	if _, err := agentHookConsent(state, "https://other.test/api", "codex", "/work/project"); err == nil {
		t.Fatal("reused another service's approval")
	}
}

func TestAgentHookRejectsUnapprovedServiceBeforeWriting(t *testing.T) {
	wrapper := hookTestExecutable(t)
	previous := turnExecutable
	turnExecutable = func() (string, error) { return wrapper, nil }
	t.Cleanup(func() { turnExecutable = previous })
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "state.json"), []byte(`{"api":"https://doomer.ai/api","grants":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOOMER_API_URL", "http://localhost:3000/api")
	c := newAgentWatchHookCommand()
	c.SetIn(strings.NewReader(`{"session_id":"test","cwd":"/tmp","hook_event_name":"UserPromptSubmit"}`))
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"--manager-home", home, "--agent", "codex"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "upload approval is required") {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "native-state")); !os.IsNotExist(err) {
		t.Fatal("unapproved hook wrote native state")
	}
}

func TestAgentHookResultsIncludeReviewLinks(t *testing.T) {
	url := "https://doomer.ai/look-closer/example/repo/review"
	result := turnHookResult(livewatch.Response{Results: []livewatch.Result{{Repository: "/repo", Status: "ready", URL: url}}})
	if !strings.Contains(result["systemMessage"].(string), url) {
		t.Fatal("review link missing")
	}
}

func hookTestExecutable(t *testing.T) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(t.TempDir(), "test-cli")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+watching.Quote(binary)+" -test.run=^TestAgentHookProcess$ -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOOMER_AGENT_HOOK_TEST_PROCESS", "1")
	return wrapper
}

func TestAgentHookLeavesDirectoryRoutingToWatch(t *testing.T) {
	wrapper := hookTestExecutable(t)
	old := turnExecutable
	turnExecutable = func() (string, error) { return wrapper, nil }
	t.Cleanup(func() { turnExecutable = old })
	home, root := t.TempDir(), t.TempDir()
	local := "http://localhost:3000/api"
	t.Setenv("DOOMER_API_URL", "https://doomer.ai/api")
	t.Setenv("DOOMER_TOKEN", "production-token")
	t.Setenv("DOOMER_CONFIG_DIR", t.TempDir())
	state := `{"version":1,"api":"https://doomer.ai/api","identities":{"http://localhost:3000/api":"local-token"},"grants":[{"scope":"global","api":"http://localhost:3000/api","approvedAt":"2026-11-12T12:00:00Z"}]}`
	if err := os.WriteFile(filepath.Join(home, "state.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".doomer.env"), []byte("DOOMER_API_URL="+local+"\nDOOMER_TOKEN=\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(watching.HookInput{Event: "UserPromptSubmit", Session: "directory-test", Turn: "one", CWD: root})
	c := NewRootCommand()
	c.SetArgs([]string{"watch", "agent-hook", "--manager-home", home, "--agent", "codex"})
	c.SetIn(bytes.NewReader(input))
	var out bytes.Buffer
	c.SetOut(&out)
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "not completed") {
		t.Fatal(out.String())
	}
	id := hookIdentity{Namespace: home, Agent: "codex", Session: "directory-test", Turn: "one"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer livewatch.Call(ctx, id.Key(), "cancel")
	response, live := livewatch.Available(ctx, id.Key())
	if !live || response.API != local {
		t.Fatalf("watch did not select localhost: %+v", response)
	}
}
