package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/watch"
)

func TestHooksInstallIsLocalIdempotentAndHooksOnly(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0700)
	original := `{"permissions":{"deny":["Bash(curl *)"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo unrelated"}]}]}}`
	os.WriteFile(path, []byte(original), 0600)
	for i := 0; i < 2; i++ {
		out, err := execute(t, "", "hooks", "install", "--agent", "claude")
		if err != nil {
			t.Fatal(out, err)
		}
		installed, err := watch.Installed(path, "--managed-task-adapter")
		if err != nil || !installed {
			t.Fatal(installed, err)
		}
		data, _ := os.ReadFile(path)
		var settings map[string]any
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "echo unrelated") {
			t.Fatal("removed unrelated hook")
		}
		permissions, _ := json.Marshal(settings["permissions"])
		if string(permissions) != `{"deny":["Bash(curl *)"]}` {
			t.Fatalf("changed permissions: %s", permissions)
		}
		if i == 0 {
			original = string(data)
		} else if string(data) != original {
			t.Fatal("repeat installation changed settings")
		}
	}
	if _, err := execute(t, "", "hooks", "install", "--agent", "unknown"); err == nil {
		t.Fatal("accepted unknown agent")
	}
	if _, err := execute(t, "", "hooks", "install", "--agent", "claude", "--task", "anything"); err == nil {
		t.Fatal("hooks-only command accepted policy installation")
	}
}
