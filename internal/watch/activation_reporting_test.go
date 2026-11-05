package watch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
)

func TestActivationRequiresRealHookAndNewConsent(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	repo, _ = filepath.EvalSymlinks(repo)
	refs := filepath.Join(repo, ".agents/skills/doomer-review/references")
	os.MkdirAll(refs, 0700)
	os.WriteFile(filepath.Join(refs, "install.md"), []byte("## Select CLI release 2026.11.5.2\n"), 0600)
	id, err := EnsureInstallationID(root)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := EnsureInstallationID(root)
	if len(id) != 64 || id != again {
		t.Fatal("installation identity was not stable")
	}
	cfg := Config{Directories: []string{repo}, InstallationID: id, RuntimeVersion: "2026.11.5.2"}
	input := HookInput{Session: "chat", Turn: "turn", CWD: repo, Event: "UserPromptSubmit", Prompt: "Update docs"}
	run := func() {
		raw, _ := json.Marshal(input)
		var out bytes.Buffer
		hookWithRepositories(context.Background(), root, cfg, bytes.NewReader(raw), &out, func(context.Context) bool { return false }, true, "codex")
	}
	run()
	identity, _ := RepositoryIdentity(context.Background(), repo)
	file := activationFile(root, identity, "codex")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("prompt alone counted as activation")
	}
	input.Event = "PreToolUse"
	input.Tool = "Bash"
	input.ToolID = "read"
	input.Input = json.RawMessage(`{"cmd":"git status --short"}`)
	run()
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("legacy consent silently enrolled")
	}
	cfg.ActivationReporting = true
	input.ToolID = "approved-read"
	run()
	var saved activationRecord
	if err := Read(file, &saved); err != nil {
		var d hookDiagnostic
		Read(filepath.Join(root, "hook-diagnostics", Key("chatPreToolUse")+".json"), &d)
		t.Fatalf("%v: diagnostic=%+v", err, d)
	}
	if saved.Event.InstallationID != id || !strings.HasPrefix(saved.Event.Repository, "local/") {
		t.Fatalf("bad activation: %+v", saved.Event)
	}
	before, _ := os.ReadFile(file)
	run()
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("repeated hook changed first activation")
	}
	// Linked worktrees must use the same outbox entry and must never expose their paths.
	linked := filepath.Join(t.TempDir(), "linked")
	if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-b", "linked", linked, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("worktree: %s %v", out, err)
	}
	linkedID, _ := RepositoryIdentity(context.Background(), linked)
	queueActivation(context.Background(), root, linked, "codex", cfg)
	if linkedID != identity {
		t.Fatal("linked identity differs")
	}
	after, _ = os.ReadFile(file)
	if !bytes.Equal(before, after) || bytes.Contains(after, []byte(repo)) || bytes.Contains(after, []byte(linked)) {
		t.Fatal("duplicate activation or leaked path")
	}
}

type activationTestClient struct {
	Client
	calls int
	fail  bool
}

func (c *activationTestClient) ReportActivation(_ context.Context, _ string, _ adversarylabs.ActivationEvent) error {
	c.calls++
	if c.fail {
		return errors.New("offline")
	}
	return nil
}

func TestActivationDeliveryIsIdempotentAndHonorsPauseAndRevocation(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	repo, _ = filepath.EvalSymlinks(repo)
	refs := filepath.Join(repo, ".agents/skills/doomer-review/references")
	os.MkdirAll(refs, 0700)
	os.WriteFile(filepath.Join(refs, "install.md"), []byte("## Select CLI release 2026.11.5.2\n"), 0600)
	id, _ := EnsureInstallationID(root)
	token := "token"
	cfg := Config{Version: 1, API: "https://example.test/api", Profile: "default", AccountKey: Key(token), ConsentedAt: time.Now(), ActivationReporting: true, InstallationID: id, RuntimeVersion: "2026.11.5.2", Directories: []string{repo}}
	Write(filepath.Join(root, "config.json"), cfg)
	queueActivation(context.Background(), root, repo, "codex", cfg)
	c := &activationTestClient{fail: true}
	r := Runner{Root: root, Config: cfg, Token: token, Client: c}
	r.reportActivations(context.Background(), repo)
	r.reportActivations(context.Background(), repo)
	if c.calls != 1 {
		t.Fatal("retry was not bounded")
	}
	identity, _ := RepositoryIdentity(context.Background(), repo)
	file := activationFile(root, identity, "codex")
	var record activationRecord
	Read(file, &record)
	record.RetryAt = time.Time{}
	Write(file, record)
	Write(filepath.Join(root, "paused.json"), true)
	r.reportActivations(context.Background(), repo)
	if c.calls != 1 {
		t.Fatal("sent while paused")
	}
	os.Remove(filepath.Join(root, "paused.json"))
	os.Remove(filepath.Join(root, "config.json"))
	r.reportActivations(context.Background(), repo)
	if c.calls != 1 {
		t.Fatal("sent after consent revoked")
	}
	Write(filepath.Join(root, "config.json"), cfg)
	c.fail = false
	r.reportActivations(context.Background(), repo)
	r.reportActivations(context.Background(), repo)
	if c.calls != 2 {
		t.Fatal("successful event sent more than once")
	}
	Read(file, &record)
	if !record.Sent {
		t.Fatal("delivery not persisted")
	}
}
