package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/doomerlabs/doomer/internal/watch"
)

func TestManagedHookDiagnostics(t *testing.T) {
	home := t.TempDir()
	c := collector{home: home}
	missing := c.hook(home, "settings.json")
	if missing["installation"] != "missing" {
		t.Fatal(missing)
	}
	os.WriteFile(filepath.Join(home, "settings.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"command":"/home/.local/bin/doomer agent-hook --agent claude --managed-task-adapter --state /private/state"}]}]}}`), 0600)
	result := c.hook(home, "settings.json")
	if result["installation"] != "partial" || result["activation"] != "unverified" {
		t.Fatal(result)
	}
	commands := result["doomer_commands"].([]any)
	if len(commands) != 1 || commands[0].(object)["agent_claude"] != true {
		t.Fatal(result)
	}
	if err := watch.Install(filepath.Join(home, "settings.json"), "doomer agent-hook --agent claude --managed-task-adapter", "--managed-task-adapter", false); err != nil {
		t.Fatal(err)
	}
	if complete := c.hook(home, "settings.json"); complete["installation"] != "installed" {
		t.Fatal(complete)
	}
}

func TestCopilotDoctorScopesHookConfiguration(t *testing.T) {
	profile, checkout := t.TempDir(), t.TempDir()
	t.Setenv("COPILOT_HOME", profile)
	c := collector{home: t.TempDir(), repo: checkout}
	hooks := map[string][]object{}
	for _, event := range []string{"userPromptSubmitted", "preToolUse", "postToolUse", "agentStop", "sessionEnd"} {
		hooks[event] = []object{{"type": "command", "command": "/private/doomer watch agent --agent=copilot --event=" + event}}
	}
	write := func(root, relative string) {
		t.Helper()
		file := filepath.Join(root, relative)
		data, err := json.Marshal(object{"version": 1, "hooks": hooks})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(checkout, ".github/hooks/doomer-node.json")
	project := c.copilotHooks()[1].(object)
	if project["active_profile_agent"] != "copilot" || project["installation"] != "installed" || project["activation"] != "unverified" {
		t.Fatalf("incorrect checkout diagnosis: %v", project)
	}
	write(profile, "hooks/doomer-node.json")
	if duplicate := c.copilotHooks()[1].(object); duplicate["duplicate_installation"] != true {
		t.Fatalf("failed to report duplicate configuration: %v", duplicate)
	}
	delete(hooks, "agentStop")
	write(checkout, ".github/hooks/doomer-node.json")
	profileResult := c.copilotHooks()[0].(object)
	if profileResult["active_profile_agent"] != "copilot" || c.copilotHooks()[1].(object)["installation"] != "partial" {
		t.Fatal("incomplete checkout config should be reported separately from the installed profile hook")
	}
}
