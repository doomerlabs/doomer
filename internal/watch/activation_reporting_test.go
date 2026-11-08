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

func TestApprovalIsSeparateFromHookActivation(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	id, _ := EnsureInstallationID(root)
	cfg := Config{ActivationReporting: true, InstallationID: id}
	QueueApproval(context.Background(), root, repo, "codex", cfg)
	identity, _ := RepositoryIdentity(context.Background(), repo)
	var record activationRecord
	if err := Read(lifecycleFile(root, identity, "codex", "approved"), &record); err != nil {
		t.Fatal(err)
	}
	if record.Event.Stage != "approved" {
		t.Fatal("wrong stage")
	}
	if _, err := os.Stat(activationFile(root, identity, "codex")); !os.IsNotExist(err) {
		t.Fatal("approval fabricated a hook")
	}
	queueActivation(context.Background(), root, repo, "codex", cfg)
	if err := Read(activationFile(root, identity, "codex"), &record); err != nil {
		t.Fatal(err)
	}
	if record.Event.Stage != "activated" {
		t.Fatal("wrong stage")
	}
}

func TestActivationKeepsApprovedRemoteNameButDropsCredentialsAndPaths(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://private-user:secret@github.com/org/repo.git?token=private#fragment").CombinedOutput(); err != nil {
		t.Fatalf("remote: %s %v", out, err)
	}
	id, _ := EnsureInstallationID(root)
	cfg := Config{ActivationReporting: true, InstallationID: id}
	queueActivation(context.Background(), root, repo, "codex", cfg)
	identity, _ := RepositoryIdentity(context.Background(), repo)
	var record activationRecord
	if err := Read(activationFile(root, identity, "codex"), &record); err != nil {
		t.Fatal(err)
	}
	if record.Event.Repository != "github.com/org/repo" {
		t.Fatalf("bad remote: %s", record.Event.Repository)
	}
	data, _ := json.Marshal(record.Event)
	for _, secret := range []string{"private-user", "secret", "token=", "fragment", repo} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("private remote data leaked")
		}
	}
}

func TestInstallationAttemptsKeepSeparateMilestonesAndOnlyReportCurrentConsent(t *testing.T) {
	root, repo := t.TempDir(), repository(t)
	repo, _ = filepath.EvalSymlinks(repo)
	id, _ := EnsureInstallationID(root)
	first, _ := NewInstallationAttemptID()
	second, _ := NewInstallationAttemptID()
	if len(first) != 64 || len(second) != 64 || first == second {
		t.Fatal("attempt IDs must be fresh")
	}
	cfg := Config{Version: 1, API: "https://example.test/api", Profile: "default", AccountKey: Key("token"), ConsentedAt: time.Now(), ActivationReporting: true, InstallationID: id, InstallationAttemptID: first, Directories: []string{repo}}
	identity, _ := RepositoryIdentity(context.Background(), repo)
	for _, attempt := range []string{first, second} {
		cfg.InstallationAttemptID = attempt
		QueueApproval(context.Background(), root, repo, "claude", cfg)
		queueActivation(context.Background(), root, repo, "claude", cfg)
		queueLifecycle(context.Background(), root, repo, "claude", "first_review", "review-"+attempt, cfg)
		for _, stage := range []string{"approved", "activated", "first_review"} {
			file := lifecycleFile(root, identity, "claude", stage, attempt)
			before, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			queueLifecycle(context.Background(), root, repo, "claude", stage, "replacement", cfg)
			after, _ := os.ReadFile(file)
			if !bytes.Equal(before, after) {
				t.Fatal("retry replaced a milestone")
			}
			var record activationRecord
			if err := Read(file, &record); err != nil || record.Event.AttemptID != attempt || record.Event.InstallationID != id {
				t.Fatalf("wrong attempt: %+v %v", record, err)
			}
		}
	}
	Write(filepath.Join(root, "config.json"), cfg)
	client := &activationTestClient{}
	runner := Runner{Root: root, Config: cfg, Token: "token", Client: client}
	runner.reportActivations(context.Background(), repo)
	runner.reportActivations(context.Background(), repo)
	if client.calls != 3 {
		t.Fatalf("reported %d milestones, want current attempt's three exactly once", client.calls)
	}
	runner.Config.InstallationAttemptID = first
	runner.reportActivations(context.Background(), repo)
	if client.calls != 3 {
		t.Fatal("old consent was reported")
	}
}

func TestReviewIdentityAndMetadataAreScopedToInstallationAttempt(t *testing.T) {
	dir := repository(t)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600)
	client := &fakeClient{}
	first, _ := NewInstallationAttemptID()
	runner := Runner{Root: t.TempDir(), Config: Config{API: "https://example.com/api", ActivationReporting: true, InstallationID: strings.Repeat("a", 64), InstallationAttemptID: first}, Client: client, Token: "token", Output: &bytes.Buffer{}, Interval: time.Second}
	requestTestAgentReview(t, &runner, dir)
	if err := runner.Run(context.Background(), dir, true); err != nil {
		t.Fatal(err)
	}
	oldReview := client.input.ReviewID
	if client.input.Client["installation_attempt_id"] != first {
		t.Fatal("missing attempt metadata")
	}
	if err := runner.Run(context.Background(), dir, true); err != nil {
		t.Fatal(err)
	}
	if client.input.ReviewID != oldReview {
		t.Fatal("retry lost its identity")
	}
	second, _ := NewInstallationAttemptID()
	runner.Config.InstallationAttemptID = second
	if err := runner.Run(context.Background(), dir, true); err != nil {
		t.Fatal(err)
	}
	if client.input.ReviewID == oldReview || client.input.Client["installation_attempt_id"] != second {
		t.Fatal("new installation reused the old review")
	}
}
