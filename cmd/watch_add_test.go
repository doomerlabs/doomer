package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWatchAddRequiresExplicitWatcher(t *testing.T) {
	for _, args := range [][]string{{"/tmp"}, {"/tmp", "--control", "not-a-key"}, {"--control", strings.Repeat("a", 64)}} {
		c := newWatchAddCommand()
		c.SetArgs(args)
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		if err := c.Execute(); err == nil {
			t.Fatalf("accepted invalid arguments %v", args)
		}
	}
}

func TestRuntimeAdvertisesWatchAdd(t *testing.T) {
	var out bytes.Buffer
	c := newRuntimeInfoCommand()
	c.SetOut(&out)
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Capabilities["watchAdd"] || result.Capabilities["hookWorkspaceConfig"] {
		t.Fatal(result.Capabilities)
	}
}

func TestHookContextRegistersOutsideRepositoriesForBothAgents(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		output := watchAgentContextForAgent(strings.Repeat("a", 64), agent, "/workspace")
		text := output["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
		if !strings.Contains(text, `Watch root: "/workspace"`) || !strings.Contains(text, "Do not run watch add for directories inside that watched tree") {
			t.Fatalf("ambiguous nested-repository scope: %s", text)
		}
		if !strings.Contains(text, "watch add /absolute/path/to/repository --control "+strings.Repeat("a", 64)) {
			t.Fatalf("missing explicit watcher registration for %s: %s", agent, text)
		}
	}
}

func TestWatchAddControlIsRegisteredOnRealCommand(t *testing.T) {
	root := NewRootCommand()
	command, _, err := root.Find([]string{"watch", "add"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Flags().Lookup("control") == nil {
		t.Fatal("watch add resolves to a command without --control")
	}
	root.SetArgs([]string{"watch", "add", "/tmp", "--control", "invalid"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "control key") {
		t.Fatalf("wrong command: %v", err)
	}
}

func TestWatchCommandSurface(t *testing.T) {
	root := NewRootCommand()
	watch, _, err := root.Find([]string{"watch"})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, c := range watch.Commands() {
		names[c.Name()] = true
	}
	for _, name := range []string{"agent", "add", "results", "findings"} {
		if !names[name] {
			t.Errorf("missing watch %s", name)
		}
	}
	for _, name := range []string{"agent-hook", "start", "stop", "ls", "report", "instructions", "respond"} {
		if names[name] {
			t.Errorf("obsolete watch %s is still registered", name)
		}
	}
	findings, _, err := root.Find([]string{"watch", "findings"})
	if err != nil {
		t.Fatal(err)
	}
	children := map[string]bool{}
	for _, c := range findings.Commands() {
		children[c.Name()] = true
	}
	if !children["ls"] || !children["respond"] || len(children) != 2 {
		t.Fatalf("unexpected findings commands: %v", children)
	}
}
