package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/doomerlabs/doomer/internal/doctor"
	watching "github.com/doomerlabs/doomer/internal/watch"
)

func TestAgentSessionProbeUsesInstalledHookPathWithoutNetwork(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("test subprocess wrapper requires a POSIX shell")
	}
	var calls atomic.Int32
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer service.Close()

	home, repo := t.TempDir(), newWatchTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "probe-fixture.txt"), []byte("Already untracked source"), 0600); err != nil {
		t.Fatal(err)
	}
	subdirectory := filepath.Join(repo, "subdirectory")
	if err := os.Mkdir(subdirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdirectory, ".doomer.env"), []byte("DOOMER_API_URL=http://127.0.0.1:1/api\n"), 0600); err != nil {
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
	t.Setenv("DOOMER_API_URL", service.URL+"/api")

	probe := func(t *testing.T, agent, cwd, want string) {
		t.Helper()
		input, err := json.Marshal(watching.HookInput{
			Event: "UserPromptSubmit", Session: "probe-session", Turn: "probe-turn", CWD: cwd,
			Prompt: doctor.SessionPrompt,
		})
		if err != nil {
			t.Fatal(err)
		}
		command := NewRootCommand()
		command.SetArgs([]string{"watch", "agent", "--manager-home", home, "--agent", agent})
		command.SetIn(bytes.NewReader(input))
		var output bytes.Buffer
		command.SetOut(&output)
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		var hook struct {
			Specific struct {
				Event   string `json:"hookEventName"`
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(output.Bytes(), &hook); err != nil || hook.Specific.Event != "UserPromptSubmit" || !strings.Contains(hook.Specific.Context, "["+want+"]") {
			t.Fatalf("expected %s from %s, got %s", want, agent, output.String())
		}
		if strings.Contains(hook.Specific.Context, "Already untracked source") {
			t.Fatal("diagnostics included source")
		}
	}
	api := service.URL + "/api"
	state := `{"version":1,"api":` + fmt.Sprintf("%q", api) + `,"identities":{` + fmt.Sprintf("%q", api) + `:"test"},"grants":[{"scope":"global","api":` + fmt.Sprintf("%q", api) + `,"approvedAt":"2026-11-12T12:00:00Z"}]}`
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			statePath := filepath.Join(home, "state.json")
			if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			probe(t, agent, t.TempDir(), "checkout_unavailable")
			probe(t, agent, repo, "upload_approval_missing")
			if err := os.WriteFile(statePath, []byte(state), 0600); err != nil {
				t.Fatal(err)
			}
			probe(t, agent, subdirectory, "upload_approval_missing")
			probe(t, agent, repo, "watcher_ready")
		})
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("session test made %d requests to the configured service", got)
	}
}
