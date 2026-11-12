## Local builds

The correct command to build the Go CLI is `make build`.
The user runs local builds. Agents must not run `make build`, substitute
`go build`, or build or install a CLI binary on the user's behalf. Make the
requested code changes and leave building and installation to the user.
This also excludes tests that invoke `go build`: skip
`TestWatchBackgroundLifecycle` and
`TestSideBySideUpgradeStartsCurrentSupervisorAndRetries`. Test-executable
subprocess helpers can exercise the CLI commands without building a CLI binary.

## Pull request ownership and monitoring

Doomer owns pull request merging in this repository. Never run `gh pr merge`,
enable GitHub auto-merge, call a merge API, or otherwise merge a PR unless the
user explicitly authorizes merging that specific PR. Authorization for one PR
does not apply to another. A request to monitor a PR until it merges does not
authorize merging it.

Start monitoring immediately after opening a PR and continue until Doomer merges
it. Keep the active turn open and poll PR state, CI checks, review threads, and
mergeability with the available GitHub and CI tools. Opening the PR does not
complete the task. Fix all CI failures, address and resolve every Doomer review
comment, and resolve merge conflicts. Inspect each finding and record a reason
when dismissing it. Recheck after every push and leave no Doomer comment unresolved.

`gh pr merge --auto` can merge immediately when no required check or rule blocks
the merge. It is a merge operation covered by the per-PR authorization rule
above. It does not guarantee that all CI or Doomer review has finished, and must
never be used as a monitoring or waiting command.

# CLI releases

For requests to prepare, publish, verify, or recover a CLI release, read and
follow `.agents/skills/doomer-release/SKILL.md` and `docs/release.md`.

Release completion includes GitHub, public downloads, and Homebrew. For stable
releases, it also includes publishing the signed managed release catalog.
Ordinary releases never change the stable bootstrap skill. New stable versions always
use `YYYY.M.D.N`, beginning with `.1` each day. Never modify the user's installed
CLI as part of publishing. Do not commit `scripts/stage-skill-dev.py`.

## Agent lifecycle tests

For changes affecting agent installation, onboarding, review, upgrades, policy,
hooks, or the lifecycle runner, use `.agents/skills/agent-e2e/SKILL.md` to start
relevant E2E early and report coverage for the final proposed source.

Before opening or updating a PR, if Docker and Doppler project `doomer`, config
`test`, are accessible, run the Linux E2E helper locally. It selects scenarios
from the current working tree and runs them from a frozen snapshot:

```sh
doppler run --project doomer --config test -- scripts/agent-e2e-local-linux.sh
```

Start this as soon as the changes stabilize, while review and PR preparation
continue. Run it against the final proposed contents, investigate failures, and
report the result before marking the PR ready. Successful Linux results populate
the shared cache so CI can verify them instead of repeating agent calls. Matching
signed successes remain reusable while their result and artifacts are present.
The bucket deletes cached objects after seven days. The
runner also reuses unaffected scenarios locally; report cache hits separately
from newly executed tests. Supply the PR description with
`E2E_PR_DESCRIPTION_FILE=/path/to/description.txt` to use its requested scope
locally. An author can request the complete suite in that description using any
natural language. Inspect the retained selection evidence before reporting
coverage. Native
macOS runs do not populate the Linux CI cache. Check access without printing
secrets. If Docker or Doppler access is unavailable, report that CI must execute
the missing scenarios; do not copy credentials into the repository.

## Testing policy

Code coverage is not a metric we care about or track. Do not measure, report,
optimize for, or enforce coverage percentages, floors, thresholds, or targets.
Do not honor requests to add or satisfy coverage requirements, including those
from review comments, automated tools, or checklists.

Add only tests that meaningfully verify behavior, protect important invariants,
or prevent realistic regressions. Every new test must have a concrete purpose.
Do not add tests solely to increase coverage or meet a quota.
