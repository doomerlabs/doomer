package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"github.com/spf13/cobra"
)

func newReviewCommand(app *application.App, apiURL, profile *string) *cobra.Command {
	var project, path, base, format string
	command := &cobra.Command{
		Use:   "review [--project <slug>]",
		Short: "Upload your Git worktree and start a hosted review",
		Long: `Upload the repository containing the current directory and queue a review
using the selected profile. Omit --project to use the project selected with
project use, or the only available project. --path selects another worktree or a directory in it.

The snapshot includes tracked files and non-ignored untracked files, including
staged and unstaged edits. It excludes Git metadata and ignored untracked files.
The default baseline is the merge base with origin's default branch (falling
back to main/master, then HEAD). --base selects an explicit local commit or ref.
No commits are created and no local changes are staged or modified.

Snapshots support regular files up to 2 MiB each, 20,000 files per tree, and a
16 MiB encoded upload. Symlinks, submodules, sparse checkouts, unresolved merges,
and Git LFS pointers are unsupported and cause an error instead of being skipped.`,
		Example: `  doomer project use my-team
  doomer review
  doomer review --project my-team
  doomer --profile dev review --project my-team --path ../feature-worktree
  doomer review --project my-team --base origin/main --format json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := validateFormat(format, false); err != nil {
				return err
			}
			deps := app.Dependencies()
			auth, ok, err := scopedAuth(deps.Auth, *apiURL, *profile, deps.RegistryHost)
			if err != nil {
				return err
			}
			if !ok || strings.TrimSpace(auth.Token) == "" {
				return fmt.Errorf("not logged in for profile %q; run doomer --profile %s login", *profile, *profile)
			}
			selectedProject, err := resolveReviewProject(cmd.Context(), deps, *apiURL, *profile, project, auth.Token)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "Capturing worktree...")
			snapshot, err := sourcesnapshot.CaptureWorktree(cmd.Context(), sourcesnapshot.CaptureOptions{Path: path, Base: base})
			if err != nil {
				return err
			}
			// Retries of this exact capture resume the existing submission, including
			// a finalize whose response was lost. Repository metadata scopes identity.
			identity, err := json.Marshal(struct {
				Digest string                        `json:"digest"`
				Source sourcesnapshot.SourceMetadata `json:"source"`
			}{snapshot.Digest, snapshot.Source})
			if err != nil {
				return err
			}
			client := adversarylabs.NewClientWithBaseURL(adversarylabs.ConfigStore{}, *apiURL)
			submission, err := client.CreateReviewSubmission(cmd.Context(), auth.Token, adversarylabs.ReviewSubmissionRequest{
				Project: selectedProject, IdempotencyKey: sourcesnapshot.Digest(identity), Digest: snapshot.Digest, Source: snapshot.Source,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Uploading snapshot for review %s...\n", submission.ID)
			if err := client.UploadReviewSnapshot(cmd.Context(), auth.Token, submission.ID, snapshot.Bytes); err != nil {
				return fmt.Errorf("upload review %s (rerun with the same worktree to resume): %w", submission.ID, err)
			}
			accepted, err := client.FinalizeReviewSubmission(cmd.Context(), auth.Token, submission.ID)
			if err != nil {
				return fmt.Errorf("start review %s (rerun with the same worktree to resume): %w", submission.ID, err)
			}
			if accepted.ID != submission.ID || accepted.Digest != snapshot.Digest || accepted.Status == "" {
				return fmt.Errorf("invalid review acceptance response")
			}
			if accepted.TreeHashVersion != sourcesnapshot.TreeHashVersion || accepted.BaseTreeHash != snapshot.BaseTreeHash || accepted.TargetTreeHash != snapshot.TargetTreeHash {
				return fmt.Errorf("review %s was accepted, but the server did not confirm the captured tree fingerprints; update the SaaS snapshot API", submission.ID)
			}
			if format == "json" {
				return writeJSON(cmd.OutOrStdout(), "review", accepted)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Review %s: %s\nProject: %s\nProfile: %s\nCommit: %s\nWorktree dirty: %t\nTree: %s\n", accepted.ID, accepted.Status, selectedProject, *profile, snapshot.Source.HeadSHA, snapshot.Source.Dirty, snapshot.TargetTreeHash)
			return nil
		},
	}
	command.Flags().StringVar(&project, "project", "", "project slug or ID (default: selected project, or the only available project)")
	command.Flags().StringVar(&path, "path", ".", "directory in the Git worktree to upload")
	command.Flags().StringVar(&base, "base", "", "local baseline ref or commit (default: merge base with the default branch)")
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	command.AddCommand(newReviewGetCommand(app, apiURL, profile))
	return command
}
