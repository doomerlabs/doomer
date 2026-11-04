package watch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/doomerlabs/doomer/internal/selfupdate"
)

func upgradeFixture(t *testing.T) (string, string, selfupdate.Updater) {
	t.Helper()
	root, repo, home := t.TempDir(), repository(t), t.TempDir()
	body := []byte("managed CLI")
	binary := filepath.Join(home, ".local/bin/doomer")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, body, 0700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	record := selfupdate.Record{SchemaVersion: 1, Manager: "doomer-review", Path: binary, Version: "2026.11.3.5", SHA256: hex.EncodeToString(digest[:])}
	if err := Write(filepath.Join(home, ".local/share/doomer/installation.json"), record); err != nil {
		t.Fatal(err)
	}
	writeSkillPin(t, repo, ".agents", "2026.11.3.6")
	return root, repo, selfupdate.Updater{Home: home}
}

func writeSkillPin(t *testing.T, repo, directory, version string) {
	t.Helper()
	path := filepath.Join(repo, directory, "skills/doomer-review/references/install.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Install\n\n## Select CLI release "+version+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHookUpgradeNoticePerSessionAndPin(t *testing.T) {
	root, repo, updater := upgradeFixture(t)
	first := localUpgradeNotice(root, repo, "existing-chat", updater)
	if !strings.Contains(first, "2026.11.3.5") || !strings.Contains(first, "2026.11.3.6") || !strings.Contains(first, "Require the user's approval") {
		t.Fatal(first)
	}
	if got := localUpgradeNotice(root, repo, "existing-chat", updater); got != "" {
		t.Fatal("repeated notice", got)
	}
	if got := localUpgradeNotice(root, repo, "new-chat", updater); got == "" {
		t.Fatal("new chat missed notice")
	}
	writeSkillPin(t, repo, ".agents", "2026.11.3.7")
	if got := localUpgradeNotice(root, repo, "existing-chat", updater); !strings.Contains(got, "2026.11.3.7") {
		t.Fatal(got)
	}
}

func TestHookUpgradeNoticeSkipsReadyUnmanagedAndConflictingPins(t *testing.T) {
	root, repo, updater := upgradeFixture(t)
	writeSkillPin(t, repo, ".agents", "2026.11.3.5")
	if got := localUpgradeNotice(root, repo, "ready", updater); got != "" {
		t.Fatal(got)
	}
	writeSkillPin(t, repo, ".agents", "2026.11.3.4")
	if got := localUpgradeNotice(root, repo, "newer", updater); got != "" {
		t.Fatal(got)
	}
	writeSkillPin(t, repo, ".agents", "2026.11.3.6")
	writeSkillPin(t, repo, ".claude", "2026.11.3.7")
	if got := localUpgradeNotice(root, repo, "conflict", updater); got != "" {
		t.Fatal(got)
	}
	writeSkillPin(t, repo, ".claude", "2026.11.3.6")
	if err := os.Remove(filepath.Join(updater.Home, ".local/share/doomer/installation.json")); err != nil {
		t.Fatal(err)
	}
	if got := localUpgradeNotice(root, repo, "unmanaged", updater); got != "" {
		t.Fatal(got)
	}
}

func TestConcurrentHookUpgradeNotice(t *testing.T) {
	root, repo, updater := upgradeFixture(t)
	var wg sync.WaitGroup
	notices := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); notices <- localUpgradeNotice(root, repo, "chat", updater) }()
	}
	wg.Wait()
	close(notices)
	count := 0
	for notice := range notices {
		if notice != "" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("got %d notices", count)
	}
}

func TestApprovedHookDeliversUpgradeWithoutRestart(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			root, repo, updater := upgradeFixture(t)
			t.Setenv("HOME", updater.Home)
			cfg := Config{Directories: []string{repo}}
			input := HookInput{Session: "already-running-chat", Turn: "current-turn", Event: "PreToolUse", CWD: repo, Tool: "Edit", ToolID: "edit", Input: json.RawMessage(`{"file_path":"file.txt"}`)}
			run := func(cfg Config) string {
				raw, _ := json.Marshal(input)
				var out strings.Builder
				hookWithReadiness(context.Background(), root, cfg, strings.NewReader(string(raw)), &out, func(context.Context) bool { return false }, agent)
				return out.String()
			}
			if got := run(Config{}); strings.Contains(got, "managed CLI") {
				t.Fatal("unapproved repo got notice", got)
			}
			got := run(cfg)
			if !strings.Contains(got, "additionalContext") || !strings.Contains(got, "2026.11.3.6") {
				t.Fatal(got)
			}
			input.ToolID = "next-edit"
			if got := run(cfg); strings.Contains(got, "managed CLI") {
				t.Fatal("notice repeated", got)
			}
		})
	}
}

func TestUpgradeNegativeCheckIsCachedUntilInstallationChanges(t *testing.T) {
	root, repo, updater := upgradeFixture(t)
	writeSkillPin(t, repo, ".agents", "2026.11.3.5")
	if got := localUpgradeNotice(root, repo, "chat", updater); got != "" {
		t.Fatal(got)
	}
	entries, err := os.ReadDir(filepath.Join(root, "upgrade-checks"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("missing negative cache: %v", err)
	}
	file := filepath.Join(root, "upgrade-checks", entries[0].Name())
	var first, second upgradeCheck
	Read(file, &first)
	localUpgradeNotice(root, repo, "chat", updater)
	Read(file, &second)
	if first != second {
		t.Fatal("unchanged install was checked again")
	}
	// A new ownership record must invalidate a ready result in the same chat.
	var record selfupdate.Record
	recordFile := filepath.Join(updater.Home, ".local/share/doomer/installation.json")
	Read(recordFile, &record)
	record.Version = "2026.11.3.4"
	if err := Write(recordFile, record); err != nil {
		t.Fatal(err)
	}
	if got := localUpgradeNotice(root, repo, "chat", updater); !strings.Contains(got, "2026.11.3.4") {
		t.Fatal("changed install missed", got)
	}
}
