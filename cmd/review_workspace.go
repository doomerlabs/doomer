package cmd

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"github.com/spf13/cobra"
)

func runWorkspaceReview(cmd *cobra.Command, clock application.Clock, api, profile, token, path string, capture sourcesnapshot.Capture, format string, look, full, showDiff bool, threshold int, waitFor reviewWaitPolicy, timeout, interval time.Duration) error {
	root, _, err := watchPaths(api, profile)
	if err != nil {
		return err
	}
	path, err = watching.Root(cmd.Context(), path)
	if err != nil {
		return err
	}
	manifest, err := sourcesnapshot.DecodeWorkspace(capture.Bytes)
	if err != nil {
		return err
	}
	diff, err := watching.Diff(cmd.Context(), manifest)
	if err != nil {
		return err
	}
	repository := capture.Source.Repository
	if repository == "" {
		repository = "local/checkout"
	}
	if format == "text" {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "\n  %s · %s\n", reviewLine(repository), shortReviewDigest(capture.TargetTreeHash)); err != nil {
			return err
		}
	}
	stats := summarizeReviewDiff(diff)
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), stats.String()); err != nil {
		return err
	}
	if showDiff {
		// Keep stdout machine-readable when --format=json is selected.
		if _, err := io.WriteString(cmd.ErrOrStderr(), diff); err != nil {
			return err
		}
	}
	client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, api)
	uploader := watching.Runner{Root: root, Config: watching.Config{API: api, Profile: profile}, Client: client, Token: token, Output: cmd.ErrOrStderr()}
	repo, base, head, err := uploader.UploadSnapshots(cmd.Context(), path, repository, manifest)
	if err != nil {
		return err
	}
	// A manual invocation requests fresh analysis. Generate once so HTTP retries
	// still use the same idempotency key; uploaded content keeps its own cache.
	reviewID := "cli-" + rand.Text()
	input := adversarylabs.WorkspaceSubmission{ReviewScope: "diff", LookCloser: &look, FullReview: &full, RepositoryID: repo, SnapshotID: head, BaseSnapshotID: base, ReviewID: reviewID, Repository: repository, Branch: capture.Source.Branch, Diff: diff, Client: map[string]string{"name": "Doomer CLI"}}
	if look {
		input.LookCloserThreshold = &threshold
	}
	accepted, err := client.StartWorkspace(cmd.Context(), token, input)
	if err != nil {
		return err
	}
	link := adversarylabs.WorkspaceLink(api, accepted)
	if look && (accepted.LookCloserThreshold == nil || *accepted.LookCloserThreshold != threshold) {
		return fmt.Errorf("server did not confirm the Look Closer threshold; update the SaaS workspace API: %s", link)
	}
	if !look && (accepted.LookCloserEnabled == nil || *accepted.LookCloserEnabled) {
		return fmt.Errorf("server did not confirm that Look Closer is disabled; update the SaaS workspace API: %s", link)
	}
	if accepted.Anonymous {
		full = false
		waitFor.lookCloser = waitFor.lookCloser || waitFor.fullReview
		waitFor.fullReview = false
	}
	if accepted.Full == nil || accepted.Full.Enabled != full {
		return fmt.Errorf("server did not confirm the requested review mode; update the SaaS workspace API: %s", link)
	}
	if !waitFor.lookCloser && !waitFor.fullReview {
		return writeWorkspaceReview(cmd.OutOrStdout(), format, accepted, link, &stats)
	}
	stage := "Look Closer"
	if waitFor.fullReview {
		stage = "deep review"
		if waitFor.lookCloser {
			stage = "Look Closer and deep review"
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Running Doomer.ai %s...\n", stage)
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	result, waitErr := waitForWorkspaceReview(ctx, clock, client, token, accepted, waitFor, interval)
	if err := writeWorkspaceReview(cmd.OutOrStdout(), format, result, link, &stats); err != nil {
		return err
	}
	if waitErr != nil {
		return waitErr
	}
	return nil
}

type workspaceStatusClient interface {
	GetWorkspace(context.Context, string, string) (adversarylabs.WorkspaceReview, error)
	GetWorkspaceResults(context.Context, string, string) (adversarylabs.WorkspaceReview, error)
}

func waitForWorkspaceReview(ctx context.Context, clock application.Clock, client workspaceStatusClient, token string, current adversarylabs.WorkspaceReview, waitFor reviewWaitPolicy, interval time.Duration) (adversarylabs.WorkspaceReview, error) {
	for {
		if err := ctx.Err(); err != nil {
			return current, err
		}
		if waitFor.fullReview && current.Full != nil && !current.Full.Enabled {
			return current, fmt.Errorf("deep review is disabled for this run")
		}
		quickDone := !waitFor.lookCloser || current.Early != nil && current.Early.Status == "ready"
		fullDone := !waitFor.fullReview || current.Status == "completed"
		if waitFor.lookCloser && current.Early != nil && current.Early.Status == "unavailable" {
			return current, fmt.Errorf("Look Closer could not complete")
		}
		if quickDone && fullDone {
			if !waitFor.fullReview {
				return current, nil
			}
			result, err := client.GetWorkspaceResults(ctx, token, current.ID)
			if err != nil {
				return current, err
			}
			return result, nil
		}
		switch current.Status {
		case "completed":
			return current, fmt.Errorf("review ended without a Look Closer result")
		case "failed", "cancelled", "expired":
			return current, fmt.Errorf("review %s", current.Status)
		}
		timer := clock.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return current, ctx.Err()
		case <-timer.C():
			timer.Stop()
		}
		next, err := client.GetWorkspace(ctx, token, current.ID)
		if err != nil {
			return current, err
		}
		current = next
	}
}

func writeWorkspaceReview(w io.Writer, format string, review adversarylabs.WorkspaceReview, link string, stats *reviewDiffStats) error {
	if format == "json" {
		return writeJSON(w, "review", struct {
			adversarylabs.WorkspaceReview
			URL  string           `json:"url"`
			Diff *reviewDiffStats `json:"diff,omitempty"`
		}{review, link, stats})
	}
	text, err := workspaceReviewText(review, link, reviewTerminal(w))
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, text)
	return err
}
