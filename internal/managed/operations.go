package managed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
)

type ReviewClient interface {
	StartWorkspace(context.Context, string, adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error)
	GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error)
	GetWorkspaceResults(context.Context, string, string) (adversarylabs.WorkspaceReview, error)
	ReportWorkspaceLead(context.Context, string, string, string, string, string, *bool) error
}

type Engine struct {
	Store   Store
	Client  ReviewClient
	Capture func(context.Context, Task) (sourcesnapshot.Capture, error)
	Upload  func(context.Context, Task, string, string, sourcesnapshot.Manifest) (string, string, string, error)
}

// Submit journals the exact request before sending it. A lost HTTP response
// retries the same server idempotency key and the same uploaded snapshots.
func (e Engine) Submit(ctx context.Context, task *Task, token string) error {
	if task.Stage == "submitting" {
		return e.send(ctx, task, token)
	}
	if task.Review != nil && (task.Review.Early == nil || task.Review.Early.Status != "ready") {
		return nil
	}
	capture, err := e.Capture(ctx, *task)
	if errors.Is(err, sourcesnapshot.ErrNoChanges) && task.Review == nil && task.Submission == nil {
		task.NoChanges = true
		task.LastError = ""
		task.Stage = "no_changes"
		return e.Store.Save(task)
	}
	if err != nil {
		return err
	}
	task.NoChanges = false
	if capture.TargetTreeHash == task.ReviewedTree && task.Review != nil {
		return nil
	}
	manifest, err := sourcesnapshot.DecodeWorkspace(capture.Bytes)
	if err != nil {
		return err
	}
	diff, err := watch.Diff(ctx, manifest)
	if err != nil {
		return err
	}
	repository := capture.Source.Repository
	if repository == "" {
		repository = "local/checkout"
	}
	repo, base, head, err := e.Upload(ctx, *task, token, repository, manifest)
	if err != nil {
		return NetworkError("snapshot_upload", err)
	}
	task.Revision++
	look := true
	threshold := 80
	prompt := []rune(task.Prompt)
	if len(prompt) > 4000 {
		prompt = prompt[:4000]
	}
	var userRequest *adversarylabs.WorkspaceUserRequest
	if strings.TrimSpace(string(prompt)) != "" {
		userRequest = &adversarylabs.WorkspaceUserRequest{Prompt: string(prompt), SessionID: task.Handle, TurnID: fmt.Sprint(task.Revision)}
	}
	task.Submission = &adversarylabs.WorkspaceSubmission{
		ReviewScope: "diff", LookCloser: &look, LookCloserThreshold: &threshold,
		RepositoryID: repo, BaseSnapshotID: base, SnapshotID: head,
		ReviewID: fmt.Sprintf("managed-%s-%d", task.Handle, task.Revision), GroupID: task.Handle, Revision: task.Revision,
		Repository: repository, Branch: capture.Source.Branch, Diff: diff, Continuity: true,
		UserRequest: userRequest,
		Client:      map[string]string{"name": "Doomer managed CLI", "agent": task.Agent, "task_id": task.Handle, "cli_version": task.Release, "bootstrap_protocol": fmt.Sprint(Protocol)},
	}
	task.ReviewedTree = capture.TargetTreeHash
	task.Stage = "submitting"
	if err = e.Store.Save(task); err != nil {
		return err
	}
	return e.send(ctx, task, token)
}
func (e Engine) send(ctx context.Context, task *Task, token string) error {
	if task.Submission == nil {
		return fmt.Errorf("missing journaled submission")
	}
	result, err := e.Client.StartWorkspace(ctx, token, *task.Submission)
	if err != nil {
		return NetworkError("review_submission", err)
	}
	if result.Anonymous {
		return fmt.Errorf("account-linked review required; server returned anonymous review")
	}
	if result.Full == nil || result.LookCloserEnabled == nil || !*result.LookCloserEnabled {
		return fmt.Errorf("server did not confirm the enabled review stages")
	}
	task.Review = &result
	task.Stage = "review_pending"
	return e.Store.Save(task)
}
func (e Engine) Status(ctx context.Context, task *Task, token string) error {
	if task.Stage == "submitting" {
		if err := e.send(ctx, task, token); err != nil {
			return err
		}
	}
	if task.Review == nil {
		return nil
	}
	result, err := e.Client.GetWorkspace(ctx, token, task.Review.ID)
	if err != nil {
		return NetworkError("review_status", err)
	}
	if result.ID != task.Review.ID || result.Anonymous {
		return fmt.Errorf("review identity mismatch")
	}
	if result.Full != nil && result.Full.Enabled && result.Full.Status == "completed" {
		result, err = e.Client.GetWorkspaceResults(ctx, token, task.Review.ID)
		if err != nil {
			return NetworkError("review_results", err)
		}
		if result.ID != task.Review.ID || result.Anonymous || result.Full == nil || !result.Full.Enabled || result.Full.Status != "completed" || result.Full.Result == nil {
			return fmt.Errorf("server did not return the completed deep review result")
		}
	}
	// Older servers omit links from compact status responses.
	if result.URL == "" {
		result.URL = task.Review.URL

	}
	task.Review = &result
	task.Stage = "review_pending"
	if result.Early != nil && (result.Early.Status == "ready") {
		task.Stage = "responses_required"
		if len(result.Early.Leads) == 0 {
			task.Stage = "reviewed"
			if result.Full != nil && result.Full.Enabled && result.Full.Status != "completed" {
				task.Stage = "review_pending"
			}
		}
	}
	deepDisabled := result.Error == "full_review_temporarily_disabled" && result.Full != nil && !result.Full.Enabled
	if result.Status == "failed" || result.Status == "cancelled" && !deepDisabled || result.Status == "expired" || result.Early != nil && (result.Early.Status == "unavailable" || result.Early.FailureCode != "") {
		task.Stage = "review_failed"
	}
	return e.Store.Save(task)
}
func ValidateResponse(r Response) error {
	if len(r.LeadID) != 64 || strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 2000 {
		return fmt.Errorf("response requires lead_id and evidence up to 2000 bytes")
	}
	switch r.Status {
	case "fixed":
		if r.Agrees == nil || !*r.Agrees {
			return fmt.Errorf("fixed requires agrees=true")
		}
	case "dismissed":
		if r.Agrees == nil || *r.Agrees {
			return fmt.Errorf("dismissed requires agrees=false")
		}
	case "deferred":
	default:
		return fmt.Errorf("unknown response status")
	}
	return nil
}
func (e Engine) Respond(ctx context.Context, task *Task, token string, items []Response) error {
	if task.Review == nil || task.Review.Early == nil || task.Review.Early.Status != "ready" || task.Review.Early.FailureCode != "" {
		return fmt.Errorf("review results are not ready")
	}
	if task.Responses == nil {
		task.Responses = map[string]Response{}
	}
	valid := map[string]bool{}
	for _, lead := range task.Review.Early.Leads {
		valid[lead.ID] = true
	}
	seen := map[string]bool{}
	for _, item := range items {
		if err := ValidateResponse(item); err != nil {
			return err
		}
		if !valid[item.LeadID] || seen[item.LeadID] {
			return fmt.Errorf("response does not identify a unique lead in this review")
		}
		seen[item.LeadID] = true
	}
	for _, item := range items {
		item.Sent = false
		task.Responses[task.Review.ID+":"+item.LeadID] = item
	}
	if err := e.Store.Save(task); err != nil {
		return err
	}
	for _, lead := range task.Review.Early.Leads {
		key := task.Review.ID + ":" + lead.ID
		item, ok := task.Responses[key]
		if !ok || item.Sent {
			continue
		}
		if err := e.Client.ReportWorkspaceLead(ctx, token, task.Review.ID, item.LeadID, item.Status, item.Reason, item.Agrees); err != nil {
			return NetworkError("finding_response", err)
		}
		item.Sent = true
		task.Responses[key] = item
		if err := e.Store.Save(task); err != nil {
			return err
		}
	}
	task.Stage = "reviewed"
	if task.Review.Full != nil && task.Review.Full.Enabled && task.Review.Full.Status != "completed" {
		task.Stage = "review_pending"
	}
	for _, lead := range task.Review.Early.Leads {
		if !task.Responses[task.Review.ID+":"+lead.ID].Sent {
			task.Stage = "responses_required"
		}
	}
	return e.Store.Save(task)
}
func (e Engine) Finish(ctx context.Context, task *Task, token string) error {
	if task.NoChanges {
		_, err := e.Capture(ctx, *task)
		if errors.Is(err, sourcesnapshot.ErrNoChanges) {
			task.Stage = "complete"
			return e.Store.Save(task)
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("checkout changed; submit this task before finishing")
	}
	if err := e.Status(ctx, task, token); err != nil {
		return err
	}
	if task.Stage == "review_failed" {
		return fmt.Errorf("review failed; inspect the task status before retrying")
	}
	if task.Stage == "review_pending" {
		return nil
	}
	if err := e.Respond(ctx, task, token, nil); err != nil {
		return err
	}
	if task.Stage != "reviewed" {
		if task.Stage == "review_pending" {
			return nil
		}
		return fmt.Errorf("record every finding response before finishing")
	}
	capture, err := e.Capture(ctx, *task)
	if err != nil {
		return err
	}
	if capture.TargetTreeHash != task.ReviewedTree {
		return fmt.Errorf("checkout changed after review; submit this task again")
	}
	task.Stage = "complete"
	task.Updated = time.Now().UTC()
	return e.Store.Save(task)
}

// Retry starts a new revision only after the previous review is terminal.
// Network retries use Submit/Status and retain their existing request identity.
func (e Engine) Retry(ctx context.Context, task *Task, token string) error {
	if err := e.Status(ctx, task, token); err != nil {
		return err
	}
	if task.Stage != "review_failed" {
		return fmt.Errorf("review is not terminal; resume its existing submission")
	}
	task.Review = nil
	task.Submission = nil
	task.ReviewedTree = ""
	task.Stage = "ready"
	if err := e.Store.Save(task); err != nil {
		return err
	}
	return e.Submit(ctx, task, token)
}
