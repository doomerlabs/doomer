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

var ErrCheckoutChanged = errors.New("checkout changed after review; submit this task again")

type ReviewClient interface {
	StartWorkspace(context.Context, string, adversarylabs.WorkspaceSubmission) (adversarylabs.WorkspaceReview, error)
	GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error)
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
	if task.Purpose == "setup" {
		task.Stage = "complete"
		task.LastError = ""
		return e.Store.Save(task)
	}
	if task.Stage == "submitting" {
		return e.send(ctx, task, token)
	}
	if task.Review != nil && (task.Review.Early == nil || task.Review.Early.Status != "ready") {
		return nil
	}
	// A tool hook can run between reading findings and sending their responses.
	// Keep that review addressable until every response has been acknowledged.
	if task.Review != nil {
		for _, lead := range task.Review.Early.Leads {
			if !task.Responses[task.Review.ID+":"+lead.ID].Sent {
				return nil
			}
		}
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
	if task.InstallationID != "" && task.InstallationAttemptID != "" {
		task.Submission.Client["installation_id"] = task.InstallationID
		task.Submission.Client["installation_attempt_id"] = task.InstallationAttemptID
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
	if result.Anonymous != adversarylabs.IsAnonymousToken(token) {
		return fmt.Errorf("review credential identity mismatch")
	}
	if result.Full == nil || result.LookCloserEnabled == nil || !*result.LookCloserEnabled {
		return fmt.Errorf("server did not confirm the enabled review stages")
	}
	if result.Anonymous && result.Full.Enabled {
		return fmt.Errorf("anonymous review cannot enable deep review")
	}
	task.Review = &result
	task.LastError = ""
	task.Stage = "review_pending"
	return e.Store.Save(task)
}
func (e Engine) Status(ctx context.Context, task *Task, token string) error {
	if task.Stage == "complete" {
		return nil
	}
	if task.Stage == "submitting" {
		if task.LastError != "" {
			return errors.New(task.LastError)
		}
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
	if result.ID != task.Review.ID || result.Anonymous != adversarylabs.IsAnonymousToken(token) || (result.Anonymous && result.Full != nil && result.Full.Enabled) {
		return fmt.Errorf("review identity mismatch")
	}
	// Older servers omit links from compact status responses.
	if result.URL == "" {
		result.URL = task.Review.URL

	}
	task.Review = &result
	task.Stage = "review_pending"
	if result.Early != nil && result.Early.Status == "ready" {
		task.Stage = "reviewed"
		for _, lead := range result.Early.Leads {
			if !task.Responses[result.ID+":"+lead.ID].Sent {
				task.Stage = "responses_required"
				break
			}
		}
	}
	// Aggregate status includes Deep. A successful Quick assessment takes
	// precedence even when Deep is still running or has failed.
	if quickReviewFailed(result) {
		task.Stage = "review_failed"
	}
	return e.Store.Save(task)
}

// quickReviewFailed ignores aggregate failures once Quick is ready. Before
// Quick is ready, terminal review statuses still indicate unavailable coverage.
func quickReviewFailed(result adversarylabs.WorkspaceReview) bool {
	if result.Early != nil {
		if result.Early.FailureCode != "" || result.Early.Status == "unavailable" || result.Early.Status == "failed" {
			return true
		}
		if result.Early.Status == "ready" {
			return false
		}
	}
	return result.Status == "failed" || result.Status == "cancelled" || result.Status == "expired"
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
	if task.Review == nil || task.Review.Early == nil || task.Review.Early.Status != "ready" {
		return fmt.Errorf("review results are not ready")
	}
	// A failed assessment can still contain actionable findings. Recording their
	// responses must not turn the incomplete review into a successful one.
	failed := quickReviewFailed(*task.Review)
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
	for _, lead := range task.Review.Early.Leads {
		if !task.Responses[task.Review.ID+":"+lead.ID].Sent {
			task.Stage = "responses_required"
		}
	}
	if failed {
		task.Stage = "review_failed"
	}
	return e.Store.Save(task)
}
func (e Engine) Finish(ctx context.Context, task *Task, token string) error {
	if task.Purpose == "setup" {
		task.Stage = "complete"
		return e.Store.Save(task)
	}
	if task.NoChanges || (task.Review == nil && task.Submission == nil) {
		_, err := e.Capture(ctx, *task)
		if errors.Is(err, sourcesnapshot.ErrNoChanges) {
			task.NoChanges = true
			task.LastError = ""
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
		return ErrCheckoutChanged
	}
	task.Stage = "complete"
	task.Updated = time.Now().UTC()
	return e.Store.Save(task)
}

// Retry resumes an interrupted submission with its existing request identity.
// A new revision is created only after a previous review has failed.
func (e Engine) Retry(ctx context.Context, task *Task, token string) error {
	if task.Stage == "complete" {
		return fmt.Errorf("task is already complete")
	}
	if task.Stage == "submitting" || task.Review == nil {
		return e.Submit(ctx, task, token)
	}
	if err := e.Status(ctx, task, token); err != nil {
		return err
	}
	if task.Stage != "review_failed" {
		if task.Stage == "reviewed" || task.Stage == "responses_required" {
			return fmt.Errorf("review has completed; record any outstanding finding responses and run doomer task finish %s", task.Handle)
		}
		return fmt.Errorf("review has not failed; check doomer task status %s and continue its existing submission", task.Handle)
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
