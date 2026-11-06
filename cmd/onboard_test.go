package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/internal/publock"
	watching "github.com/doomerlabs/doomer/internal/watch"
)

func TestOnboardReusesConsentAndWatcherWithoutLosingInstructions(t *testing.T) {
	for _, client := range []struct{ agent, originator string }{{"codex", "codex_cli_rs"}, {"codex", "Codex Desktop"}, {"claude", ""}} {
		t.Run(client.agent+"/"+client.originator, func(t *testing.T) {
			agent := client.agent
			t.Setenv("CODEX_INTERNAL_ORIGINATOR_OVERRIDE", client.originator)
			store := isolate(t)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			api, profile := "http://localhost:3000/api", "onboard-test"
			app := &application.App{Deps: application.Dependencies{Auth: store}}
			repo := newWatchTestRepo(t)
			setup := newWatchCommand(app, &api, &profile)
			setup.SetContext(context.Background())
			setup.SetOut(&bytes.Buffer{})
			setup.Flags().Set("yes", "true")
			setup.Flags().Set("agent", agent)
			cfg, _, err := prepareWatch(setup, app, &api, &profile, []string{repo})
			if err != nil {
				t.Fatal(err)
			}
			root, _, _ := watchPaths(api, profile)
			lock, err := publock.TryAcquire(root, "watcher")
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			watching.Write(filepath.Join(root, "process.json"), watchProcess{PID: os.Getpid(), ConsentedAt: cfg.ConsentedAt})
			watching.Write(filepath.Join(root, "heartbeat.json"), time.Now())
			name := "AGENTS.md"
			if agent == "claude" {
				name = "CLAUDE.md"
			}
			os.WriteFile(filepath.Join(repo, name), []byte("Keep existing rules.\n"), 0600)
			// Any unexpected browser reapproval fails this test.
			previous := openSetupBrowser
			defer func() { openSetupBrowser = previous }()
			openSetupBrowser = func(context.Context, string) error { t.Fatal("repeated consent"); return nil }
			c := newOnboardCommand(app, &api, &profile)
			c.SetContext(context.Background())
			var output bytes.Buffer
			c.SetOut(&output)
			c.ParseFlags([]string{"--agent", agent, "--repo", repo, "--session", "session", "--task-stdin"})
			c.SetIn(strings.NewReader("Add pagination"))
			if err := c.RunE(c, nil); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "Reusing the running watcher") || strings.Contains(output.String(), "One restart left") {
				t.Fatal(output.String())
			}
			instructions, _ := os.ReadFile(filepath.Join(repo, name))
			if !strings.HasPrefix(string(instructions), "Keep existing rules.") {
				t.Fatal(string(instructions))
			}
			// The explicit restart path saves task context in the same onboarding call.
			c.Flags().Set("restart", "true")
			c.SetIn(strings.NewReader("Add pagination"))
			output.Reset()
			if err := c.RunE(c, nil); err != nil {
				t.Fatal(err)
			}
			expected := onboardingResumeCommand(repo, agent, "session")
			if agent == "claude" {
				expected = "--check-hooks"
			} else if client.originator == "Codex Desktop" {
				expected = "Review hooks beside the composer"
				if !strings.Contains(output.String(), watching.ResumeHandoffPrompt) || !strings.Contains(output.String(), "Your task is saved") {
					t.Fatal("missing in-chat task restoration instructions", output.String())
				}
				for _, unexpected := range []string{"One restart left", "Ctrl+C", "codex resume"} {
					if strings.Contains(output.String(), unexpected) {
						t.Fatalf("desktop setup includes terminal instructions: %s", output.String())
					}
				}
			}
			if !strings.Contains(output.String(), expected) {
				t.Fatal(output.String())
			}
			expectedTask := "Add pagination"
			if original, err := watching.ResumeTaskContext(root, repo, "session"); err != nil || original != expectedTask {
				t.Fatalf("lost restart task: %q %v", original, err)
			}
			c.SetIn(strings.NewReader("Add pagination"))
			watching.Write(filepath.Join(root, "paused.json"), true)
			if err := c.RunE(c, nil); err == nil {
				t.Fatal("onboarding resumed a paused watcher")
			}
		})
	}
}

func TestCodexCheckHooksUsesRealReceiptWithoutChangingSetup(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", "codex-session")
	api, profile := "http://localhost:3000/api", "codex-check"
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	repo := newWatchTestRepo(t)
	setup := newWatchCommand(app, &api, &profile)
	setup.SetContext(context.Background())
	setup.SetOut(io.Discard)
	setup.Flags().Set("yes", "true")
	setup.Flags().Set("agent", "codex")
	cfg, _, err := prepareWatch(setup, app, &api, &profile, []string{repo})
	if err != nil {
		t.Fatal(err)
	}
	root, config, _ := watchPaths(api, profile)
	lock, err := publock.TryAcquire(root, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	watching.Write(filepath.Join(root, "process.json"), watchProcess{PID: os.Getpid(), ConsentedAt: cfg.ConsentedAt})
	watching.Write(filepath.Join(root, "heartbeat.json"), time.Now())
	before, _ := os.ReadFile(config)
	watching.Write(filepath.Join(root, "watch-session.json"), "watcher-session")
	watching.RegisterRepository(root, repo, "explicit", true, time.Time{})
	watching.SetWatchRuntime(root, repo, watching.WatchRuntime{Session: "watcher-session", Token: "token", Status: "Watching"})
	check := func(want string) {
		t.Helper()
		watching.Write(filepath.Join(root, "heartbeat.json"), time.Now())
		c := newOnboardCommand(app, &api, &profile)
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetArgs([]string{"--repo", repo, "--agent", "codex", "--check-hooks"})
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
		var result struct{ Status, Session, Repository string }
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Status != want || result.Session != "codex-session" || result.Repository != repo {
			t.Fatalf("got %s (%v), want %s", out.String(), err, want)
		}
		after, _ := os.ReadFile(config)
		if !bytes.Equal(before, after) {
			t.Fatal("activation check changed setup")
		}
	}
	check("pending")
	check("pending")
	for _, event := range []string{"UserPromptSubmit", "PreToolUse"} {
		input := watching.HookInput{Session: "codex-session", Turn: "turn", CWD: repo, Event: event, Prompt: "Inspect repository", Tool: "Bash", ToolID: "read"}
		raw, _ := json.Marshal(input)
		watching.Hook(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard, "codex")
	}
	check("ready")
	// A new worktree inherits the repository grant, but its watcher has not
	// started. Activation should wait for the first tool instead of asking
	// the user to repeat setup.
	linked := filepath.Join(t.TempDir(), "new-linked-checkout")
	reviewGit(t, repo, "worktree", "add", "--detach", linked, "HEAD")
	repo, err = watching.Root(context.Background(), linked)
	if err != nil {
		t.Fatal(err)
	}
	if err := watching.RegisterRepository(root, repo, "linked worktree", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	check("pending")
	for _, event := range []string{"UserPromptSubmit", "PreToolUse"} {
		input := watching.HookInput{Session: "codex-session", Turn: "linked-turn", CWD: repo, Event: event, Prompt: "Inspect linked repository", Tool: "Bash", ToolID: "linked-read"}
		raw, _ := json.Marshal(input)
		watching.Hook(context.Background(), root, cfg, bytes.NewReader(raw), io.Discard, "codex")
	}
	check("pending")
	if err := watching.SetWatchRuntime(root, repo, watching.WatchRuntime{Session: "watcher-session", Token: "linked-token", Status: "Watching"}); err != nil {
		t.Fatal(err)
	}
	check("ready")
	if err := os.WriteFile(watching.TurnPath(root, "codex-session"), []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := watching.SetWatchRuntime(root, repo, watching.WatchRuntime{Session: "watcher-session", Token: "linked-token", Status: "Starting"}); err != nil {
		t.Fatal(err)
	}
	// A genuine receipt error must survive the transient watcher state.
	check("error")
	watching.Write(filepath.Join(root, "paused.json"), true)
	check("error")
}

func TestHookTrustInstructionsMatchCodexClient(t *testing.T) {
	for _, originator := range []string{"", "codex_cli_rs", "Codex Desktop"} {
		t.Run(originator, func(t *testing.T) {
			t.Setenv("CODEX_INTERNAL_ORIGINATOR_OVERRIDE", originator)
			want := "/hooks"
			if originator == "Codex Desktop" {
				want = "Settings > Hooks"
			}
			if got := hookTrustInstructions("codex"); !strings.Contains(got, want) {
				t.Fatalf("missing %q in %q", want, got)
			}
		})
	}
}

func TestOnboardingResumeCommands(t *testing.T) {
	for _, tc := range []struct{ agent, session, want string }{
		{"codex", "session", "codex resume 'session'"},
		{"codex", "", "codex resume --last"},
		{"claude", "claude-session", "claude --resume 'claude-session'"},
		{"claude", "", "claude --continue"},
	} {
		command := onboardingResumeCommand("/repo with spaces", tc.agent, tc.session)
		if command != "cd '/repo with spaces' && "+tc.want+" '"+watching.ResumeHandoffPrompt+"'" {
			t.Fatal(command)
		}
	}
}

func TestClaudeDoesNotInheritCodexSession(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "unrelated-codex-session")
	api, profile := "http://localhost:3000/api", "test"
	c := newOnboardCommand(&application.App{}, &api, &profile)
	c.SetContext(context.Background())
	c.ParseFlags([]string{"--agent", "claude", "--repo", newWatchTestRepo(t), "--task-stdin"})
	c.SetIn(strings.NewReader("Add pagination"))
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "requires a session") {
		t.Fatalf("got %v", err)
	}
}

func TestOnboardRefreshDoesNotRewriteTaskHooksOrConsent(t *testing.T) {
	store := isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	api, profile := "http://localhost:3000/api", "refresh-test"
	app := &application.App{Deps: application.Dependencies{Auth: store}}
	repo := newWatchTestRepo(t)
	setup := newWatchCommand(app, &api, &profile)
	setup.SetContext(context.Background())
	setup.SetOut(&bytes.Buffer{})
	if err := setup.Flags().Set("yes", "true"); err != nil {
		t.Fatal(err)
	}
	if err := setup.Flags().Set("agent", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareWatch(setup, app, &api, &profile, []string{repo}); err != nil {
		t.Fatal(err)
	}
	root, config, err := watchPaths(api, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := watching.SaveTaskHandoff(root, repo, "session", strings.NewReader("original task")); err != nil {
		t.Fatal(err)
	}
	if err := watching.BeginClaudeActivation(root, repo, "session", "original task"); err != nil {
		t.Fatal(err)
	}
	guidance := filepath.Join(repo, "CLAUDE.md")
	if err := os.WriteFile(guidance, []byte("keep instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	paths := []string{config, guidance,
		filepath.Join(root, "task-handoffs", watching.Key(repo+"\x00session")+".json"),
		filepath.Join(root, "onboarding", watching.Key(repo+"\x00session")+".json"),
		watching.HookFile(os.Getenv("HOME"), "claude"),
	}
	snapshots := map[string][]byte{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshots[path] = data
	}
	oldBrowser := openSetupBrowser
	t.Cleanup(func() { openSetupBrowser = oldBrowser })
	openSetupBrowser = func(context.Context, string) error { t.Fatal("refresh reopened consent"); return nil }
	c := newOnboardCommand(app, &api, &profile)
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetArgs([]string{"--repo", repo, "--agent", "claude", "--session", "session", "--refresh"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not_running") || strings.Contains(out.String(), "restart") {
		t.Fatal(out.String())
	}
	for path, before := range snapshots {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("refresh changed %s: %v", path, err)
		}
	}
}
func TestOnboardRefreshRejectsTaskAndRestartModes(t *testing.T) {
	for _, flag := range []string{"--restart", "--task-stdin", "--check-hooks"} {
		c := newOnboardCommand(&application.App{}, new(string), new(string))
		c.SetArgs([]string{"--refresh", flag})
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "none of the others") {
			t.Fatalf("accepted refresh with %s or wrong error: %v", flag, err)
		}
	}
}
