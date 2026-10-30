package cmd

import (
	"fmt"
	"time"

	"github.com/doomerlabs/doomer/internal/application"
	"github.com/doomerlabs/doomer/pkg/adversarylabs"
	"github.com/doomerlabs/doomer/pkg/sourcesnapshot"
	"github.com/spf13/cobra"
)

func newReviewCommand(app *application.App, apiURL, profile *string) *cobra.Command {
	var path, base, format string
	var lookCloser, fullReview, showDiff bool
	var waitMode string
	var lookCloserThreshold int
	var timeout, pollInterval time.Duration
	command := &cobra.Command{
		Use:   "review",
		Short: "Return Look Closer results and start a deep review of your worktree",
		Long: `Upload the repository containing the current directory and queue a review
using the selected profile. The server detects a project from the repository's
Git remote and your account's access. With no unique match it uses account-level
configuration. --path selects another worktree or a directory in it.

The snapshot includes tracked files and non-ignored untracked files, including
staged and unstaged edits. It excludes Git metadata and ignored untracked files.
The default baseline is the merge base with origin's default branch (falling
back to main/master, then HEAD). --base selects an explicit local commit or ref.
No commits are created and no local changes are staged or modified.
--show-diff prints the exact patch before upload (off by default).

Without a login, invoking review authorizes source uploads and runs free
anonymous Look Closer only. Sign in to also run Deep review.
By default Look Closer results return synchronously while the deep review
continues on the server. Look Closer defaults to a threshold of 80/100;
--look-closer-threshold=55 returns more tentative leads. This does not change deep review.
--deep-review=false or --look-closer=false skips a stage.
--wait=none returns after submission; --wait=look-closer or --wait=deep waits only
for that stage. --wait=all (or bare --wait) waits for every enabled stage.
With Look Closer disabled, the default is not to wait for the deep review.

The default path reuses uploaded content, with a 20 MiB per-file and 1 GiB total
source limit. All review modes support 20,000 files per tree. Symlinks, submodules, sparse checkouts, unresolved merges,
and Git LFS pointers are unsupported and cause an error instead of being skipped.`,
		Example: `  doomer review
  doomer review --deep-review=false
  doomer review --look-closer=false --wait=deep
  doomer --profile dev review --path ../feature-worktree
  doomer review --base origin/main --format json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := validateFormat(format, false); err != nil {
				return err
			}
			if lookCloserThreshold < 0 || lookCloserThreshold > 100 {
				return fmt.Errorf("--look-closer-threshold must be an integer from 0 to 100")
			}
			if !lookCloser && !fullReview {
				return fmt.Errorf("enable at least one review stage")
			}
			deps := app.Dependencies()
			auth, err := reviewAuth(app, *apiURL, *profile)
			if err != nil {
				return err
			}
			anonymous := adversarylabs.IsAnonymousToken(auth.Token)
			effectiveWait := waitMode
			if anonymous {
				lookCloser, fullReview = true, false
				if effectiveWait == "deep" || effectiveWait == "all" || effectiveWait == "true" {
					effectiveWait = "look-closer"
				}
			}
			waitFor, err := resolveReviewWait(effectiveWait, cmd.Flags().Changed("wait"), lookCloser, fullReview)
			if err != nil {
				return err
			}
			if waitFor.fullReview && !cmd.Flags().Changed("timeout") {
				timeout = 30 * time.Minute
			}
			if (cmd.Flags().Changed("timeout") || cmd.Flags().Changed("poll-interval")) && !waitFor.fullReview && !waitFor.lookCloser {
				return fmt.Errorf("--timeout and --poll-interval require waiting for a review stage")
			}
			if timeout <= 0 || pollInterval <= 0 {
				return fmt.Errorf("--timeout and --poll-interval must be positive")
			}
			snapshot, err := sourcesnapshot.CaptureWorktree(cmd.Context(), sourcesnapshot.CaptureOptions{Path: path, Base: base, Workspace: true})
			if err != nil {
				return err
			}
			return runWorkspaceReview(cmd, deps.Clock, *apiURL, *profile, auth.Token, path, snapshot, format, lookCloser, fullReview, showDiff, lookCloserThreshold, waitFor, timeout, pollInterval)
		},
	}
	command.Flags().StringVar(&path, "path", ".", "directory in the Git worktree to upload")
	command.Flags().StringVar(&base, "base", "", "local baseline ref or commit (default: merge base with the default branch)")
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	command.Flags().BoolVar(&lookCloser, "look-closer", true, "run Look Closer")
	command.Flags().IntVar(&lookCloserThreshold, "look-closer-threshold", 80, "minimum score for a Look Closer lead (0–100; lower returns more leads)")
	command.Flags().BoolVar(&fullReview, "deep-review", true, "also run the deep review on the server")
	command.Flags().BoolVar(&showDiff, "show-diff", false, "print the exact review diff before uploading")
	command.Flags().StringVar(&waitMode, "wait", "look-closer", "wait for none, look-closer, deep, or all (bare --wait means all)")
	command.Flags().Lookup("wait").NoOptDefVal = "all"
	command.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "maximum wait after submission (30m when waiting for deep review)")
	command.Flags().DurationVar(&pollInterval, "poll-interval", time.Second, "time between status requests")
	command.AddCommand(newReviewGetCommand(app, apiURL, profile))
	return command
}
