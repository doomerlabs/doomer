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
	submissions   []adversarylabs.WorkspaceSubmission
	lost          bool
	ids           []string
	resultFetches int
	feedback      int
	result        adversarylabs.WorkspaceReview
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
	f.resultFetches++
	return adversarylabs.WorkspaceReview{}, errors.New("Deep results must not be fetched")
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
	resumed.LastError = "recorded submission failure"
	attempts := len(client.ids)
	if err := engine.Status(context.Background(), &resumed, "token"); err == nil || len(client.ids) != attempts {
		t.Fatal("status retried a recorded failed submission")
	}
	if err = engine.Retry(context.Background(), &resumed, "token"); err != nil {
		t.Fatal(err)
	}
	if resumed.LastError != "" {
		t.Fatal("successful retry retained failure", resumed.LastError)
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
	if err := engine.Retry(context.Background(), &resumed, "token"); err == nil || !strings.Contains(err.Error(), "review has completed") || !strings.Contains(err.Error(), "task finish") {
		t.Fatal("retry must preserve the completed review and explain how to finish", err)
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

func TestSubmitPreservesReviewUntilFindingResponsesAreSent(t *testing.T) {
	store := Store{Root: t.TempDir()}
	handle, _ := NewHandle()
	first, second := strings.Repeat("a", 64), strings.Repeat("b", 64)
	review := adversarylabs.WorkspaceReview{ID: "original-review", Early: &adversarylabs.WorkspaceAssessment{
		Status: "ready", Leads: []adversarylabs.WorkspaceLead{{ID: first}, {ID: second}},
	}}
	task := Task{Schema: Protocol, Handle: handle, Stage: "responses_required", Revision: 1, Review: &review, ReviewedTree: "old-tree"}
	yes, no := true, false
	client := &fakeReview{result: adversarylabs.WorkspaceReview{ID: "next-review", LookCloserEnabled: &yes, Full: &adversarylabs.WorkspaceFullReview{}}}
	manifest := sourcesnapshot.Manifest{Version: 1, Scope: "worktree", Base: []sourcesnapshot.Entry{}, Target: []sourcesnapshot.Entry{}, Blobs: map[string][]byte{}}
	content := []byte("changed\n")
	hash := sourcesnapshot.Digest(content)
	manifest.Target = []sourcesnapshot.Entry{{Path: "file.txt", Hash: hash, Mode: "100644"}}
	manifest.Blobs[hash] = content
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	captures := 0
	engine := Engine{Store: store, Client: client, Capture: func(context.Context, Task) (sourcesnapshot.Capture, error) {
		captures++
		return sourcesnapshot.Capture{Bytes: raw, TargetTreeHash: "new-tree"}, nil
	}, Upload: func(context.Context, Task, string, string, sourcesnapshot.Manifest) (string, string, string, error) {
		return "repo", "base", "head", nil
	}}
	// PostToolUse runs after edits and can also run while the agent obtains
	// approval to send its response. Neither may replace the reviewed revision.
	for _, responses := range [][]Response{nil, {{LeadID: first, Status: "dismissed", Agrees: &no, Reason: "Checked the implementation"}}} {
		if responses != nil {
			if err := engine.Respond(context.Background(), &task, "token", responses); err != nil {
				t.Fatal(err)
			}
		}
		if err := engine.Submit(context.Background(), &task, "token"); err != nil {
			t.Fatal(err)
		}
		if captures != 0 || len(client.submissions) != 0 || task.Review.ID != review.ID || task.Revision != 1 || task.ReviewedTree != "old-tree" {
			t.Fatal("replaced review with outstanding responses", task)
		}
	}
	// The guard also applies after restoring the task's on-disk journal.
	task, err = store.Load(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Respond(context.Background(), &task, "token", []Response{{LeadID: second, Status: "fixed", Agrees: &yes, Reason: "Fixed and verified"}}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Submit(context.Background(), &task, "token"); err != nil {
		t.Fatal(err)
	}
	if captures != 1 || len(client.submissions) != 1 || client.feedback != 2 || task.Review.ID != "next-review" || task.Revision != 2 {
		t.Fatal("did not review new edits after sending responses", task)
	}
}

func TestFailedAssessmentAcceptsResponsesWithoutCompleting(t *testing.T) {
	store := Store{Root: t.TempDir()}
	handle, _ := NewHandle()
	lead := strings.Repeat("a", 64)
	result := adversarylabs.WorkspaceReview{ID: "failed-review", Status: "completed", Early: &adversarylabs.WorkspaceAssessment{
		Status: "ready", FailureCode: "model_request_failed", Leads: []adversarylabs.WorkspaceLead{{ID: lead}},
	}}
	task := Task{Schema: Protocol, Handle: handle, Stage: "review_failed", Review: &result}
	client := &fakeReview{result: result}
	engine := Engine{Store: store, Client: client}
	if err := engine.Submit(context.Background(), &task, "token"); err != nil {
		t.Fatal(err)
	}
	no := false
	if err := engine.Respond(context.Background(), &task, "token", []Response{{LeadID: lead, Status: "dismissed", Agrees: &no, Reason: "Verified compatibility"}}); err != nil {
		t.Fatal(err)
	}
	if client.feedback != 1 || !task.Responses[result.ID+":"+lead].Sent || task.Stage != "review_failed" {
		t.Fatal("failed review lost its response or failure state", task)
	}
	if err := engine.Finish(context.Background(), &task, "token"); err == nil {
		t.Fatal("response incorrectly completed a failed review")
	}
}

func TestManagedCompletionGatesOnQuickOnly(t *testing.T) {
	for _, deep := range []struct{ status, aggregate string }{
		{"queued", "queued"}, {"running", "running"}, {"completed", "completed"},
		{"failed", "failed"}, {"disabled", "cancelled"},
	} {
		for _, scenario := range []string{"ready", "outstanding", "responded", "pending", "failed", "unavailable", "failure_code", "changed"} {
			t.Run(deep.status+"/"+scenario, func(t *testing.T) {
				handle, _ := NewHandle()
				lead := strings.Repeat("d", 64)
				result := adversarylabs.WorkspaceReview{ID: "review", Status: deep.aggregate,
					Early: &adversarylabs.WorkspaceAssessment{Status: "ready"},
					Full:  &adversarylabs.WorkspaceFullReview{Enabled: deep.status != "disabled", Status: deep.status}}
				if scenario == "outstanding" || scenario == "responded" {
					result.Early.Leads = []adversarylabs.WorkspaceLead{{ID: lead}}
				}
				switch scenario {
				case "pending":
					result.Early.Status = "running"
				case "failed":
					result.Early.Status = "failed"
				case "unavailable":
					result.Early.Status = "unavailable"
				case "failure_code":
					result.Early.FailureCode = "model_request_failed"
				}
				client := &fakeReview{result: result}
				task := Task{Schema: Protocol, Handle: handle, Stage: "review_pending", Review: &result, ReviewedTree: "tree"}
				captures := 0
				engine := Engine{Store: Store{Root: t.TempDir()}, Client: client, Capture: func(context.Context, Task) (sourcesnapshot.Capture, error) {
					captures++
					tree := "tree"
					if scenario == "changed" {
						tree = "new-tree"
					}
					return sourcesnapshot.Capture{TargetTreeHash: tree}, nil
				}}
				if err := engine.Status(context.Background(), &task, "token"); err != nil {
					t.Fatal(err)
				}
				if scenario == "responded" {
					no := false
					if err := engine.Respond(context.Background(), &task, "token", []Response{{LeadID: lead, Status: "dismissed", Agrees: &no, Reason: "Verified expected behavior"}}); err != nil {
						t.Fatal(err)
					}
					if task.Stage != "reviewed" || client.feedback != 1 {
						t.Fatal(task.Stage, client.feedback)
					}
				}
				err := engine.Finish(context.Background(), &task, "token")
				switch scenario {
				case "ready", "responded":
					if err != nil || task.Stage != "complete" || captures != 1 {
						t.Fatal(task.Stage, captures, err)
					}
				case "outstanding":
					if err == nil || task.Stage != "responses_required" || captures != 0 {
						t.Fatal(task.Stage, captures, err)
					}
				case "changed":
					if !errors.Is(err, ErrCheckoutChanged) || task.Stage == "complete" {
						t.Fatal(task.Stage, err)
					}
				case "pending":
					if task.Stage == "complete" || captures != 0 {
						t.Fatal(task.Stage, captures, err)
					}
				default:
					if err == nil || task.Stage != "review_failed" || captures != 0 {
						t.Fatal(task.Stage, captures, err)
					}
				}
				if client.resultFetches != 0 {
					t.Fatal("fetched Deep results")
				}
			})
		}
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

func TestStatusPreservesCompletedTask(t *testing.T) {
	for _, review := range []*adversarylabs.WorkspaceReview{nil, {ID: "completed-review"}} {
		task := Task{Stage: "complete", Review: review}
		// A completed task already retains its final result. Status must not fetch
		// another result and demote it back to reviewed or pending.
		engine := Engine{}
		if err := engine.Status(context.Background(), &task, "token"); err != nil || task.Stage != "complete" {
			t.Fatal(task, err)
		}
	}
}
