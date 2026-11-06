package managed

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

func TestBootstrapPoliciesAndPrecedence(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	write := func(root, dir string, p Policy) {
		t.Helper()
		text, err := BootstrapText(p)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, dir, "skills/doomer-review/SKILL.md")
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	stable := Policy{Protocol: Protocol, Channel: "stable"}
	pinned := Policy{Protocol: Protocol, Channel: "pinned", Version: "2026.11.6.1"}
	write(home, ".agents", stable)
	b, err := ResolveBootstrap(repo, home, "codex")
	if err != nil || b.Scope != "personal" {
		t.Fatal(b, err)
	}
	write(repo, ".agents", pinned)
	write(repo, ".claude", pinned)
	b, err = ResolveBootstrap(repo, home, "claude")
	if err != nil || b.Policy != pinned || b.Scope != "project" {
		t.Fatal(b, err)
	}
	write(repo, ".claude", stable)
	if _, err = ResolveBootstrap(repo, home, "codex"); err == nil {
		t.Fatal("accepted conflicting project policies")
	}
	for _, bad := range []Policy{{Protocol: 1, Channel: "stable"}, {Protocol: Protocol, Channel: "latest"}, {Protocol: Protocol, Channel: "pinned", Version: "../evil"}, {Protocol: Protocol, Channel: "delayed"}} {
		if bad.Validate() == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
func TestSignedCatalogAndDelayedSelection(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	catalog := Catalog{Schema: Protocol, Generated: now, Expires: now.Add(time.Hour), Releases: []Release{
		{Version: "2026.11.6.1", Published: now.Add(-48 * time.Hour), ManifestSHA256: strings.Repeat("a", 64), Protocol: Protocol},
		{Version: "2026.11.6.2", Published: now, ManifestSHA256: strings.Repeat("b", 64), Protocol: Protocol},
	}}
	payload, _ := json.Marshal(catalog)
	raw, _ := json.Marshal(SignedCatalog{base64.StdEncoding.EncodeToString(payload), base64.StdEncoding.EncodeToString(ed25519.Sign(private, payload))})
	got, err := VerifyCatalog(raw, base64.StdEncoding.EncodeToString(public), now)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectRelease(got, Policy{Protocol: Protocol, Channel: "delayed", DelayHours: 24}, now)
	if err != nil || selected.Version != "2026.11.6.1" {
		t.Fatal(selected, err)
	}
	selected, err = SelectRelease(got, Policy{Protocol: Protocol, Channel: "stable"}, now)
	if err != nil || selected.Version != "2026.11.6.2" {
		t.Fatal(selected, err)
	}
	if _, err = VerifyCatalog(raw, base64.StdEncoding.EncodeToString(public), now.Add(2*time.Hour)); err == nil {
		t.Fatal("accepted expired catalog")
	}
	raw[len(raw)/2] ^= 1
	if _, err = VerifyCatalog(raw, base64.StdEncoding.EncodeToString(public), now); err == nil {
		t.Fatal("accepted altered catalog")
	}
	if !VersionLess("2026.11.6.9", "2026.11.6.10") {
		t.Fatal("lexical version ordering")
	}
}

type fakeReview struct {
	submissions []adversarylabs.WorkspaceSubmission
	lost        bool
	ids         []string
	feedback    int
	result      adversarylabs.WorkspaceReview
}

func (f *fakeReview) StartWorkspace(_ context.Context, _ string, s adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error) {
	f.ids = append(f.ids, s.ReviewID)
	f.submissions = append(f.submissions, s)
	if f.lost {
		f.lost = false
		return adversarylabs.WorkspaceReview{}, errors.New("response lost")
	}
	return f.result, nil
}
func (f *fakeReview) GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error) {
	return f.result, nil
}

func (f *fakeReview) GetWorkspaceResults(ctx context.Context, token, id string) (adversarylabs.WorkspaceReview, error) {
	return f.GetWorkspace(ctx, token, id)
}
func (f *fakeReview) ReportWorkspaceLead(context.Context, string, string, string, string, string, *bool) error {
	f.feedback++
	return nil
}
func TestInterruptedTaskResumesSameReviewAndRecordsResponses(t *testing.T) {
	store := Store{Root: t.TempDir()}
	handle, _ := NewHandle()
	task := Task{Schema: Protocol, Handle: handle, Repo: t.TempDir(), Release: "2026.11.6.1", Stage: "ready"}
	manifest := sourcesnapshot.Manifest{Version: 1, Scope: "worktree", Base: []sourcesnapshot.Entry{}, Target: []sourcesnapshot.Entry{}, Blobs: map[string][]byte{}}
	content := []byte("changed\n")
	hash := sourcesnapshot.Digest(content)
	manifest.Target = []sourcesnapshot.Entry{{Path: "file.txt", Hash: hash, Mode: "100644"}}
	manifest.Blobs[hash] = content
	raw, _ := json.Marshal(manifest)
	capture := sourcesnapshot.Capture{Bytes: raw, TargetTreeHash: "tree-a"}
	yes, no := true, false
	client := &fakeReview{lost: true, result: adversarylabs.WorkspaceReview{ID: "review-id", LookCloserEnabled: &yes, Full: &adversarylabs.WorkspaceFullReview{Enabled: false}, Early: &adversarylabs.WorkspaceAssessment{Status: "ready", Leads: []adversarylabs.WorkspaceLead{{ID: strings.Repeat("a", 64)}}}}}
	engine := Engine{Store: store, Client: client, Capture: func(context.Context, Task) (sourcesnapshot.Capture, error) { return capture, nil }, Upload: func(context.Context, Task, string, string, sourcesnapshot.Manifest) (string, string, string, error) {
		return "repo", "base", "head", nil
	}}
	if err := store.Save(&task); err != nil {
		t.Fatal(err)
	}
	if engine.Submit(context.Background(), &task, "token") == nil {
		t.Fatal("lost response not reported")
	}
	// Throw away all process-local task state and reconstruct the engine's input.
	resumed, err := store.Load(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.Submit(context.Background(), &resumed, "token"); err != nil {
		t.Fatal(err)
	}
	if len(client.ids) != 2 || client.ids[0] != client.ids[1] {
		t.Fatal("submission id changed on resume", client.ids)
	}
	resumed.Review.URL = "https://doomer.test/look-closer/owner/repo/" + strings.Repeat("a", 64)
	if err = engine.Status(context.Background(), &resumed, "token"); err != nil {
		t.Fatal(err)
	}
	if resumed.Review.URL == "" {
		t.Fatal("compact status discarded the review URL")
	}
	if err = engine.Finish(context.Background(), &resumed, "token"); err == nil {
		t.Fatal("completed with missing finding response")
	}
	if err = engine.Respond(context.Background(), &resumed, "token", []Response{{LeadID: strings.Repeat("a", 64), Status: "dismissed", Reason: "verified", Agrees: &no}}); err != nil {
		t.Fatal(err)
	}
	if err = engine.Finish(context.Background(), &resumed, "token"); err != nil {
		t.Fatal(err)
	}
	if resumed.Stage != "complete" || client.feedback != 1 {
		t.Fatal(resumed.Stage, client.feedback)
	}
	capture.TargetTreeHash = "tree-b"
	if err = engine.Finish(context.Background(), &resumed, "token"); err == nil {
		t.Fatal("accepted unreviewed edits")
	}
	if err := engine.Retry(context.Background(), &resumed, "token"); err == nil {
		t.Fatal("retry replaced a nonterminal review")
	}
	client.result.Status = "failed"
	client.result.Early = nil
	if err := engine.Retry(context.Background(), &resumed, "token"); err != nil {
		t.Fatal(err)
	}
	if resumed.Revision != 2 || client.ids[len(client.ids)-1] == client.ids[0] {
		t.Fatal("terminal retry did not create a new revision")
	}
	if resumed.Release != "2026.11.6.1" {
		t.Fatal("task release drifted")
	}
}

func TestManagedAutomaticDeepReviewWaitsForFullResult(t *testing.T) {
	store := Store{Root: t.TempDir()}
	handle, _ := NewHandle()
	yes := true
	client := &fakeReview{result: adversarylabs.WorkspaceReview{ID: "review", Status: "running", LookCloserEnabled: &yes,
		Early: &adversarylabs.WorkspaceAssessment{Status: "ready"}, Full: &adversarylabs.WorkspaceFullReview{Enabled: true, Status: "running"}}}
	task := Task{Schema: Protocol, Handle: handle, Review: &client.result, ReviewedTree: "tree"}
	engine := Engine{Store: store, Client: client, Capture: func(context.Context, Task) (sourcesnapshot.Capture, error) {
		return sourcesnapshot.Capture{TargetTreeHash: "tree"}, nil
	}}
	if err := engine.Finish(context.Background(), &task, "token"); err != nil || task.Stage != "review_pending" {
		t.Fatal(task.Stage, err)
	}
	leadID := strings.Repeat("d", 64)
	client.result.Early.Leads = []adversarylabs.WorkspaceLead{{ID: leadID}}
	agrees := false
	if err := engine.Respond(context.Background(), &task, "token", []Response{{LeadID: leadID, Status: "dismissed", Agrees: &agrees, Reason: "Verified expected behavior"}}); err != nil || task.Stage != "review_pending" {
		t.Fatal("responding to Quick bypassed pending Deep", task.Stage, err)
	}
	if err := engine.Finish(context.Background(), &task, "token"); err != nil || task.Stage != "review_pending" {
		t.Fatal(task.Stage, err)
	}
	client.result.Status = "completed"
	client.result.Full.Status = "completed"
	client.result.Full.Result = &adversarylabs.ReviewResult{Summary: "Deep reviewed"}
	if err := engine.Finish(context.Background(), &task, "token"); err != nil || task.Stage != "complete" || task.Review.Full.Result == nil {
		t.Fatal(task.Stage, err)
	}
	client.result.Status = "failed"
	client.result.Full.Status = "failed"
	task.Stage = "review_pending"
	if err := engine.Finish(context.Background(), &task, "token"); err == nil {
		t.Fatal("completed failed deep review")
	}
	if task.Stage != "review_failed" {
		t.Fatal("failed Deep review was not retained", task.Stage)
	}
}
func TestTaskIsolationAndNetworkRedaction(t *testing.T) {
	s := Store{Root: t.TempDir()}
	first, _ := NewHandle()
	second, _ := NewHandle()
	for i, h := range []string{first, second} {
		task := Task{Schema: Protocol, Handle: h, Release: []string{"2026.11.6.1", "2026.11.6.2"}[i]}
		if err := s.Save(&task); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := s.Load(first)
	b, _ := s.Load(second)
	if a.Release == b.Release {
		t.Fatal("task versions mixed")
	}
	lock, err := s.Lock(first)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if other, err := s.Lock(first); err == nil {
		other.Close()
		t.Fatal("allowed concurrent task mutation")
	}
	if _, err = s.Load("../../bad"); err == nil {
		t.Fatal("accepted traversal")
	}
	failure := NetworkError("upload", &url.Error{Op: "PUT", URL: "https://storage.example/private?token=secret", Err: errors.New("denied")}).Error()
	if strings.Contains(failure, "secret") || !strings.Contains(failure, "storage.example") {
		t.Fatal(failure)
	}
}

func TestReadyAssessmentWithFailureCodeCannotFinish(t *testing.T) {
	store := Store{Root: t.TempDir()}
	handle, _ := NewHandle()
	result := adversarylabs.WorkspaceReview{ID: "failed-review", Status: "completed", Early: &adversarylabs.WorkspaceAssessment{Status: "ready", FailureCode: "model_request_failed"}}
	task := Task{Schema: Protocol, Handle: handle, Review: &result}
	engine := Engine{Store: store, Client: &fakeReview{result: result}}
	if err := engine.Status(context.Background(), &task, "token"); err != nil || task.Stage != "review_failed" {
		t.Fatal("analysis failure was not terminal", task.Stage, err)
	}
	if err := engine.Finish(context.Background(), &task, "token"); err == nil {
		t.Fatal("failed analysis was reported as complete")
	}
}

func TestSubmissionOmitsUnavailablePrompt(t *testing.T) {
	for _, prompt := range []string{"", "  ", "Fix the background"} {
		t.Run(fmt.Sprintf("prompt-%d", len(prompt)), func(t *testing.T) {
			store := Store{Root: t.TempDir()}
			handle, _ := NewHandle()
			task := Task{Schema: Protocol, Handle: handle, Repo: t.TempDir(), Prompt: prompt, Stage: "ready"}
			manifest := sourcesnapshot.Manifest{Version: 1, Scope: "worktree", Base: []sourcesnapshot.Entry{}, Target: []sourcesnapshot.Entry{}, Blobs: map[string][]byte{}}
			content := []byte("changed\n")
			hash := sourcesnapshot.Digest(content)
			manifest.Target = []sourcesnapshot.Entry{{Path: "file.txt", Hash: hash, Mode: "100644"}}
			manifest.Blobs[hash] = content
			raw, _ := json.Marshal(manifest)
			yes := true
			client := &fakeReview{result: adversarylabs.WorkspaceReview{ID: "review", LookCloserEnabled: &yes, Full: &adversarylabs.WorkspaceFullReview{Enabled: false}}}
			engine := Engine{Store: store, Client: client, Capture: func(context.Context, Task) (sourcesnapshot.Capture, error) {
				return sourcesnapshot.Capture{Bytes: raw, TargetTreeHash: "tree"}, nil
			}, Upload: func(context.Context, Task, string, string, sourcesnapshot.Manifest) (string, string, string, error) {
				return "repo", "base", "head", nil
			}}
			if err := engine.Submit(context.Background(), &task, "token"); err != nil {
				t.Fatal(err)
			}
			sent := client.submissions[0].UserRequest
			if strings.TrimSpace(prompt) == "" {
				if sent != nil {
					t.Fatal("sent empty user request", sent)
				}
				wire, _ := json.Marshal(client.submissions[0])
				if strings.Contains(string(wire), "user_request") {
					t.Fatal(string(wire))
				}
			} else if sent == nil || sent.Prompt != prompt {
				t.Fatal(sent)
			}
		})
	}
}
