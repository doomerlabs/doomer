package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/publock"
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
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

func TestAgentHookStartsAndReusesGoWatcher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("background watchers require Unix")
	}
	home := t.TempDir()
	repo := newWatchTestRepo(t)
	config := filepath.Join(home, "native-state")
	api := "http://localhost:1/api" // No request should be needed before a prompt.
	root := filepath.Join(config, "watch", watching.Key(api+"\x00node-codex"))
	state := agentHookState{Identities: map[string]string{api: "doomer_anon_test_identity"}}
	cfg := watching.Config{Version: 1, API: api, Profile: "node-codex", AllChanges: true, AllRepositories: true, ExplicitConsent: true, ConsentedAt: time.Now()}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(home, "go-test-cli")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+watching.Quote(executable)+" -test.run=^TestAgentHookProcess$ -- \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOOMER_AGENT_HOOK_TEST_PROCESS", "1")
	t.Setenv("DOOMER_TOKEN", "")
	t.Cleanup(func() {
		_ = watching.Write(filepath.Join(root, "paused.json"), true)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := waitWatch(ctx, func() (bool, error) { _, live, err := runningWatch(root); return !live, err }); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := prepareAgentWatcher(ctx, wrapper, home, root, config, repo, state, &cfg); err != nil {
		t.Fatal(err)
	}
	first, live, err := runningWatch(root)
	if err != nil || !live || !first.AllChanges {
		t.Fatalf("watcher: %+v %t %v", first, live, err)
	}
	if err := prepareAgentWatcher(ctx, wrapper, home, root, config, repo, state, &cfg); err != nil {
		t.Fatal(err)
	}
	second, live, err := runningWatch(root)
	if err != nil || !live || first.PID != second.PID {
		t.Fatalf("watcher was not reused: %+v %v", second, err)
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

func TestAgentHookPreparesNativeStateWithoutNode(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "native-state")
	api := "http://localhost:3000/api"
	root := filepath.Join(config, "watch", watching.Key(api+"\x00node-codex"))
	state := agentHookState{Identities: map[string]string{api: "doomer_anon_test_identity"}}
	cfg := watching.Config{Version: 1, API: api, Profile: "node-codex", AllChanges: true, AllRepositories: true, ExplicitConsent: true, ConsentedAt: time.Now()}
	t.Setenv("DOOMER_TOKEN", "")
	if err := prepareAgentWatcher(context.Background(), "/no-node-or-executable-needed", home, root, config, "", state, &cfg); err != nil {
		t.Fatal(err)
	}
	store := adversarylabs.ConfigStore{Path: filepath.Join(config, "config.json")}
	auth, ok, err := store.ExactAuthE(adversarylabs.AuthKey(api, "node-codex"))
	if err != nil || !ok || auth.Token != state.Identities[api] {
		t.Fatalf("credentials: %v %v", ok, err)
	}
	var saved watching.Config
	if err := watching.Read(filepath.Join(root, "config.json"), &saved); err != nil {
		t.Fatal(err)
	}
	if !watching.ConsentValid(saved, api, "node-codex", auth.Token) {
		t.Fatal("native consent is not usable")
	}
	// Existing watches must not silently switch identity during another session.
	watchLock, err := publock.TryAcquire(root, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	defer watchLock.Close()
	if err := watching.Write(filepath.Join(root, "process.json"), watchProcess{AllChanges: true}); err != nil {
		t.Fatal(err)
	}
	if err := watching.Write(filepath.Join(root, "heartbeat.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOOMER_TOKEN", "different-token")
	if err := prepareAgentWatcher(context.Background(), "unused", home, root, config, "", state, &cfg); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("identity switch: %v", err)
	}
	// A paused watcher remains paused even if a fresh prompt arrives.
	if err := watching.Write(filepath.Join(root, "paused.json"), true); err != nil {
		t.Fatal(err)
	}
	if err := prepareAgentWatcher(context.Background(), "unused", home, root, config, "", state, &cfg); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("pause: %v", err)
	}
}

func TestAgentHookRejectsUnapprovedServiceBeforeWriting(t *testing.T) {
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
