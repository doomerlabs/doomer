package watch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func repository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git: %s %v", b, e)
		}
	}
	git("init", "-b", "main")
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\ntwo\n"), 0600)
	git("add", ".")
	git("-c", "core.hooksPath=/dev/null", "commit", "-m", "base")
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
func TestInstallPreservesOtherHooksAndUninstallsOnlyOwn(t *testing.T) {
	file := filepath.Join(t.TempDir(), "hooks.json")
	original := `{"keep":true,"hooks":{"Stop":[{"hooks":[{"type":"command","command":"other-command"}]}]}}`
	os.WriteFile(file, []byte(original), 0600)
	for range 2 {
		if e := Install(file, "doomer watch hook --state '/own'", "watch hook --state '/own'", false); e != nil {
			t.Fatal(e)
		}
	}
	var got map[string]any
	if e := Read(file, &got); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(got)
	if strings.Count(string(b), "other-command") != 1 || strings.Count(string(b), "doomer watch hook") != 4 {
		t.Fatal(string(b))
	}
	if e := Install(file, "", "watch hook --state '/own'", true); e != nil {
		t.Fatal(e)
	}
	b, _ = os.ReadFile(file)
	if strings.Contains(string(b), "doomer watch") || !strings.Contains(string(b), "other-command") {
		t.Fatal(string(b))
	}
}
func TestDiffMatchesAddedDeletedAndChangedFiles(t *testing.T) {
	dir := repository(t)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("one\nthree\n"), 0600)
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0600)
	c, e := sourcesnapshot.CaptureWorktree(context.Background(), sourcesnapshot.CaptureOptions{Path: dir})
	if e != nil {
		t.Fatal(e)
	}
	m, _ := sourcesnapshot.Decode(c.Bytes)
	d, e := Diff(context.Background(), m)
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"diff --git a/file.txt b/file.txt", "-two", "+three", "+++ b/new.txt", "\\ No newline at end of file"} {
		if !strings.Contains(d, want) {
			t.Fatalf("missing %s: %s", want, d)
		}
	}
	if strings.Contains(d, dir) {
		t.Fatal("temporary paths leaked")
	}
}
func TestHookOwnedDeliveryAndResponseCollection(t *testing.T) {
	dir := repository(t)
	root := t.TempDir()
	cfg := Config{Directories: []string{dir}}
	run := func(input HookInput) string {
		b, _ := json.Marshal(input)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var out strings.Builder
		Hook(ctx, root, cfg, strings.NewReader(string(b)), &out)
		return out.String()
	}
	base := HookInput{Session: "chat-a", Turn: "turn-a", CWD: dir, Event: "UserPromptSubmit", Prompt: "change it"}
	run(base)
	edit := base
	edit.Event = "PreToolUse"
	edit.ToolID = "edit-1"
	edit.Tool = "apply_patch"
	run(edit)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600)
	digest, e := snapshotDigest(context.Background(), dir)
	if e != nil {
		t.Fatal(e)
	}
	id := strings.Repeat("a", 64)
	Write(FeedPath(root, dir), Feed{Owner: Owner{base.Session, base.Turn}, Fingerprint: digest, ReviewID: "review", URL: "https://example.com/review", Updated: time.Now(), AssessmentStatus: "ready", Leads: []Lead{{ID: id, Path: "file.txt", Line: 1, Concern: "Check change", Check: "run test"}}})
	Write(filepath.Join(root, "heartbeat.json"), time.Now())
	edit.Event = "PostToolUse"
	out := run(edit)
	if !strings.Contains(out, "Generate up to five") {
		t.Fatal(out)
	}
	var requestState State
	if err := Read(StatePath(root, dir), &requestState); err != nil {
		t.Fatal(err)
	}
	if err := SubmitHypotheses(context.Background(), root, requestState.QuestionRequest, cfg, strings.NewReader(`[]`)); err != nil {
		t.Fatal(err)
	}
	base.Event = "Stop"
	out = run(base)
	if !strings.Contains(out, "Doomer.ai flagged") {
		t.Fatal(out)
	}
	other := base
	other.Session = "chat-b"
	other.Event = "Stop"
	if out = run(other); strings.Contains(out, "Doomer.ai flagged") {
		t.Fatal("cross-chat delivery")
	}
	base.Event = "Stop"
	base.Message = "Doomer 1 — Dismissed: Verified behavior is intentional."
	run(base)
	files, _ := os.ReadDir(filepath.Join(root, "feedback"))
	if len(files) != 1 {
		t.Fatal("response not queued")
	}
	var report Feedback
	Read(filepath.Join(root, "feedback", files[0].Name()), &report)
	if report.Agrees == nil || *report.Agrees || report.ReviewID != "review" {
		t.Fatalf("incorrect response %+v", report)
	}
	if out = run(base); strings.Contains(out, "Doomer.ai flagged") {
		t.Fatal("duplicate delivery")
	}
}
func TestHookWithoutWatcherReturnsImmediately(t *testing.T) {
	dir := repository(t)
	root := t.TempDir()
	cfg := Config{Directories: []string{dir}}
	input := HookInput{Session: "chat", Turn: "turn", CWD: dir, Event: "Stop"}
	b, _ := json.Marshal(input)
	start := time.Now()
	var out strings.Builder
	Hook(context.Background(), root, cfg, strings.NewReader(string(b)), &out)
	if strings.TrimSpace(out.String()) != "{}" || time.Since(start) > time.Second {
		t.Fatal(out.String())
	}
}
func TestUnknownCommandCannotClaimItsWrites(t *testing.T) {
	if mutates(HookInput{Tool: "Bash", Input: json.RawMessage(`{"command":"npm run build"}`)}) {
		t.Fatal("unknown build claimed edits")
	}
	if !mutates(HookInput{Tool: "apply_patch"}) {
		t.Fatal("patch not recognized")
	}
}

type fakeClient struct {
	reviewStatus     string
	noAssessment     bool
	assessmentStatus string
	starts           int
	input            adversarylabs.WorkspaceSubmission
	reports          int
}

func (f *fakeClient) UploadWorkspaceTree(context.Context, string, string, []sourcesnapshot.Entry, map[string][]byte) (string, string, error) {
	return "repo", "snapshot", nil
}
func (f *fakeClient) StartWorkspace(_ context.Context, _ string, in adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	f.starts++
	f.input = in
	if f.noAssessment {
		return adversarylabs.WorkspaceReview{ID: "review", Status: f.reviewStatus}, nil
	}
	status := f.assessmentStatus
	if status == "" {
		status = "ready"
	}
	return adversarylabs.WorkspaceReview{ID: "review", Status: "running", Early: &adversarylabs.WorkspaceAssessment{Status: status}}, nil
}
func (f *fakeClient) GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error) {
	panic("unexpected poll")
}
func (f *fakeClient) ReportWorkspaceLead(context.Context, string, string, string, string, string, *bool) error {
	f.reports++
	return nil
}
func TestWatchOnceUsesWorkspaceAndStableRetryIdentity(t *testing.T) {
	dir := repository(t)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600)
	client := &fakeClient{}
	r := Runner{Root: t.TempDir(), Config: Config{API: "https://example.com/api"}, Client: client, Token: "token", Output: io.Discard, Interval: time.Second}
	requestTestAgentReview(t, &r, dir)
	for range 2 {
		if e := r.Run(context.Background(), dir, true); e != nil {
			t.Fatal(e)
		}
	}
	if client.starts != 2 || client.input.Revision != 1 || !client.input.Continuity {
		t.Fatalf("bad retries %+v", client.input)
	}
}

func TestAllRepositoryConsentAndDiscovery(t *testing.T) {
	dir := repository(t)
	root := t.TempDir()
	if Allowed(Config{}, dir) {
		t.Fatal("missing approval trusted repo")
	}
	cfg := Config{AllRepositories: true}
	if !Allowed(cfg, dir) {
		t.Fatal("all-repository approval not applied")
	}
	input := HookInput{Session: "chat", Turn: "turn", CWD: dir, Event: "UserPromptSubmit"}
	b, _ := json.Marshal(input)
	var out strings.Builder
	Hook(context.Background(), root, cfg, strings.NewReader(string(b)), &out)
	var discovered string
	if Read(filepath.Join(root, "discovered", Key(dir)+".json"), &discovered) != nil || discovered != dir {
		t.Fatal("agent repository was not discovered")
	}
}

func TestWatchReportsDetectionAndGatesDiagnostics(t *testing.T) {
	dir := repository(t)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("PRIVATE-SOURCE-CONTENT\n"), 0600)
	for _, debug := range []bool{false, true} {
		var output strings.Builder
		client := &fakeClient{}
		r := Runner{Root: t.TempDir(), Config: Config{API: "https://example.com/api"}, Client: client, Token: "PRIVATE-TOKEN", Output: &output, Interval: time.Second, Debug: debug}
		requestTestAgentReview(t, &r, dir)
		if err := r.Run(context.Background(), dir, true); err != nil {
			t.Fatal(err)
		}
		log := output.String()
		for _, message := range []string{"change detected", "uploading snapshots", "look closer review requested", "look closer review completed", "https://example.com/worktree/reviews/review"} {
			if !strings.Contains(log, message) {
				t.Fatalf("missing %q: %s", message, log)
			}
		}
		if strings.Contains(log, "[debug") != debug {
			t.Fatalf("diagnostics should require --debug: %s", log)
		}
		if strings.Contains(log, "PRIVATE-SOURCE-CONTENT") || strings.Contains(log, "PRIVATE-TOKEN") {
			t.Fatal("sensitive content logged")
		}
		if debug {
			for _, message := range []string{"scanning working tree", "starting review revision", "look closer review=ready"} {
				if !strings.Contains(log, message) {
					t.Fatalf("missing %q: %s", message, log)
				}
			}
		}
	}
}

func TestWatchUnavailableAssessmentIsNotReportedAsReady(t *testing.T) {
	dir := repository(t)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600)
	var output strings.Builder
	r := Runner{Root: t.TempDir(), Config: Config{API: "https://example.com/api"}, Client: &fakeClient{assessmentStatus: "unavailable"}, Output: &output, Interval: time.Second}
	requestTestAgentReview(t, &r, dir)
	if err := r.Run(context.Background(), dir, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "look closer review unavailable") || strings.Contains(output.String(), "look closer review completed") {
		t.Fatal(output.String())
	}
}

func TestWatchDoesNotRetryTerminalReviewWithoutAssessment(t *testing.T) {
	dir := repository(t)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("changed\n"), 0600)
	var output strings.Builder
	client := &fakeClient{reviewStatus: "completed", noAssessment: true}
	r := Runner{Root: t.TempDir(), Config: Config{API: "https://example.com/api"}, Client: client, Output: &output, Interval: time.Second}
	requestTestAgentReview(t, &r, dir)
	err := r.Run(context.Background(), dir, true)
	if !errors.Is(err, errReviewTerminal) {
		t.Fatalf("expected terminal review, got %v", err)
	}
	if strings.Contains(output.String(), "retrying later") || !strings.Contains(output.String(), "Waiting for a new change") {
		t.Fatal(output.String())
	}
	if !strings.Contains(output.String(), "full review completed") {
		t.Fatal(output.String())
	}
}

// Seed the same request produced by a completed agent edit; observing dirty
// files by itself must never authorize a runner to upload them.
func requestTestAgentReview(t *testing.T, r *Runner, path string) {
	t.Helper()
	fingerprint, err := snapshotDigest(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	owner := Owner{Session: "agent-session", Turn: "agent-turn"}
	r.StartedAt = time.Now().Add(-time.Second)
	if err := Write(TurnPath(r.Root, owner.Session), Turn{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	if err := Write(StatePath(r.Root, path), State{Owner: owner, Fingerprint: fingerprint, RequestedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestWatchStartupAndManualEditsDoNotUpload(t *testing.T) {
	dir := repository(t)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("existing local change\n"), 0600)
	var output strings.Builder
	client := &fakeClient{}
	r := Runner{Root: t.TempDir(), Client: client, Output: &output, Interval: time.Second, Debug: true}
	if err := r.Run(context.Background(), dir, true); err != nil {
		t.Fatal(err)
	}
	if client.starts != 0 || strings.Contains(output.String(), "uploading snapshots") {
		t.Fatal(output.String())
	}
	// Even a valid old agent request must not cause a review on watcher restart.
	requestTestAgentReview(t, &r, dir)
	r.StartedAt = time.Now().Add(time.Second)
	if err := r.Run(context.Background(), dir, true); err != nil {
		t.Fatal(err)
	}
	if client.starts != 0 {
		t.Fatal("replayed request from before watch started")
	}
	r.StartedAt = time.Now().Add(-time.Minute)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("manual edit after old agent request\n"), 0600)
	if err := r.Run(context.Background(), dir, true); err != nil {
		t.Fatal(err)
	}
	if client.starts != 0 {
		t.Fatal("manual edit reused another snapshot's request")
	}
}

func TestAgentRequestRequiresCurrentUnambiguousTurn(t *testing.T) {
	root, path := t.TempDir(), "/repo"
	since := time.Now().Add(-time.Second)
	owner := Owner{Session: "chat", Turn: "turn"}
	base := State{Owner: owner, Fingerprint: "snapshot", RequestedAt: time.Now()}
	Write(TurnPath(root, owner.Session), Turn{Owner: owner})
	for _, tc := range []struct {
		name    string
		mutate  func(*State)
		allowed bool
	}{
		{"completed edit", func(*State) {}, true},
		{"no edit", func(s *State) { s.RequestedAt = time.Time{} }, false},
		{"old request", func(s *State) { s.RequestedAt = since.Add(-time.Second) }, false},
		{"manual edit", func(s *State) { s.Fingerprint = "other" }, false},
		{"unknown owner", func(s *State) { s.Owner = Owner{} }, false},
		{"another turn", func(s *State) { s.Owner.Turn = "other" }, false},
		{"ambiguous", func(s *State) { s.Ambiguous = true }, false},
		{"still editing", func(s *State) { s.Active = map[string]Operation{"edit": {Owner: owner}} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := base
			tc.mutate(&state)
			if err := Write(StatePath(root, path), state); err != nil {
				t.Fatal(err)
			}
			if got := agentReviewRequested(root, path, "snapshot", since); got != tc.allowed {
				t.Fatalf("allowed=%v", got)
			}
		})
	}
}

func TestAgentEditCanTriggerReviewWithExistingDirtyFiles(t *testing.T) {
	dir, root := repository(t), t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("pre-existing dirty files\n"), 0600)
	since := time.Now()
	input := HookInput{Session: "chat", Turn: "turn", CWD: dir, Event: "UserPromptSubmit", Prompt: "edit this"}
	run := func() {
		b, _ := json.Marshal(input)
		Hook(context.Background(), root, Config{Directories: []string{dir}}, strings.NewReader(string(b)), io.Discard)
	}
	run()
	fingerprint, _ := snapshotDigest(context.Background(), dir)
	if agentReviewRequested(root, dir, fingerprint, since) {
		t.Fatal("prompt alone triggered review")
	}
	input.Event = "PreToolUse"
	input.Tool = "apply_patch"
	input.ToolID = "edit"
	run()
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("agent edited existing dirty files\n"), 0600)
	input.Event = "PostToolUse"
	run()
	fingerprint, _ = snapshotDigest(context.Background(), dir)
	if !agentReviewRequested(root, dir, fingerprint, since) {
		t.Fatal("agent edit did not request review")
	}
}

func TestHookRecordsExplicitCodingAgent(t *testing.T) {
	for _, agent := range []string{"codex", "claude", "unsupported"} {
		t.Run(agent, func(t *testing.T) {
			dir := repository(t)
			root := t.TempDir()
			cfg := Config{Directories: []string{dir}}
			input := HookInput{Session: "session", Turn: "turn", CWD: dir, Event: "UserPromptSubmit", Prompt: "Update docs"}
			raw, _ := json.Marshal(input)
			Hook(context.Background(), root, cfg, strings.NewReader(string(raw)), io.Discard, agent)
			var turn Turn
			if err := Read(TurnPath(root, "session"), &turn); err != nil {
				t.Fatal(err)
			}
			want := agent
			if want == "unsupported" {
				want = "other"
			}
			if turn.Agent != want {
				t.Fatalf("agent=%q want %q", turn.Agent, want)
			}
		})
	}
}

func TestHookWaitsOnlyOncePerEditButStopStillWaits(t *testing.T) {
	dir := repository(t)
	root := t.TempDir()
	owner := Owner{Session: "chat", Turn: "turn"}
	cfg := Config{Directories: []string{dir}}
	if err := Write(TurnPath(root, owner.Session), Turn{Owner: owner}); err != nil {
		t.Fatal(err)
	}
	if err := Write(StatePath(root, dir), State{Owner: owner, RequestedAt: time.Now(), HypothesesSubmitted: true}); err != nil {
		t.Fatal(err)
	}
	if err := Write(filepath.Join(root, "heartbeat.json"), time.Now()); err != nil {
		t.Fatal(err)
	}
	run := func(event string) error {
		raw, _ := json.Marshal(HookInput{Session: owner.Session, Turn: owner.Turn, CWD: dir, Event: event, Tool: "read_file"})
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		Hook(ctx, root, cfg, strings.NewReader(string(raw)), io.Discard)
		return ctx.Err()
	}
	if err := run("PostToolUse"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first inline hook should wait: %v", err)
	}
	if err := run("PostToolUse"); err != nil {
		t.Fatalf("subsequent inline hook should return without waiting: %v", err)
	}
	if err := run("Stop"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop should retain its bounded wait: %v", err)
	}
}
