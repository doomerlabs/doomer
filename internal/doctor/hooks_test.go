package doctor

import (
	"github.com/doomerlabs/doomer/internal/watch"
	"os"
	"path/filepath"
	"testing"
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
