## Local builds

The correct command to build the Go CLI is `make build`.
Use `make dev` to build and atomically replace `~/.local/bin/doomer-dev`.
Agents may run `make build` and automated tests, including E2E tests in isolated
environments. Do not run `make dev`, replace installed binaries, change live
configuration, start services in the user's development environment, or stop the
user's running instances. Agents may start and stop processes and containers they
create solely for tests.

## Development VM: production and Goblin isolation

This is a development-only note for the `/home/exedev` VM, not a customer
installation guide or automatic provisioning instruction.

Two separate installations share this host:

| Context | CLI and manager | Service and credentials |
| --- | --- | --- |
| `/home/exedev/workspace` | Released `~/.local/bin/doomer`; normal `~/.local/share/doomer-node` manager, skill and hooks | `https://doomer.ai/api`; `~/.doomer-developer/doomer-production` |
| `/home/exedev/test-apps/goblin-dispatch` | Local `~/.local/share/doomer-node-dev/bin/doomer-dev`; separate development manager, skill and hooks | `http://localhost:3000/api`; `~/.doomer-developer/doomer-testing` |

In an interactive Bash shell, plain `claude` automatically uses the development
profile in `~/test-apps/goblin-dispatch` and its subdirectories; elsewhere it uses
the normal production profile. This selection is a function in `~/.bashrc`.
`goblin-claude` is a launcher command, not a directory. It can also be invoked
explicitly from elsewhere and changes into Goblin Dispatch. Directly invoking
the Claude executable bypasses the shell function.

The Goblin launcher uses `~/.doomer-developer/goblin-home` as its private `HOME`
and sets `CLAUDE_CONFIG_DIR` to that home's `.claude`. This isolates skill
discovery as well as hooks. Production keeps the normal home and configuration.
Each profile must have only one Doomer handler per hook event. Do not install
both production and development handlers into the same profile.

User-run maintenance commands:

```sh
# Released installation only:
npx @doomerlabs/doomer@beta update

# Rebuild CLI and refresh manager, skill and hooks in Goblin's isolated profile:
~/.local/bin/update-goblin-doomer

# Refresh using the existing workspace/doomer/bin/doomer build:
~/.local/bin/update-goblin-doomer --reuse-build
```

The development updater uses the sibling `doomer`, `doomer-node`, and `skills`
checkouts under `~/workspace`. The full updater builds into the development
manager's build directory. `--reuse-build` may install older bytes if the
workspace build has not been refreshed. Start a fresh Claude session after
updating its skill or hooks. Existing processes may still use earlier binaries.

Do not use the older shared `scripts/install-local.sh` workflow on this VM or
repoint `~/.local/bin/doomer` at a development binary: that breaks isolation.
For live Goblin updates, use the dedicated updater only when the user authorizes
installation. Normal builds and isolated tests remain allowed under Local builds.
Do not restart the user's development server or worker without authorization.

`~/workspace/.doomer.env` routes workspace reviews to production;
`~/test-apps/.doomer.env` routes test-app reviews to localhost. These files alone
do not select the CLI, manager, skill or hooks. Preserve both credential stores.

For read-only verification, inspect the shell function/launcher, both manager
state files and both profiles' hooks, then check effective routing:

```sh
~/.local/bin/doomer watch diagnose ~/workspace --agent claude \
  --manager-home ~/.local/share/doomer-node
~/.local/share/doomer-node-dev/bin/doomer-dev watch diagnose \
  ~/test-apps/goblin-dispatch --agent claude \
  --manager-home ~/.local/share/doomer-node-dev
```

Confirm the resolved executable, service and upload approval. Do not equate
configuration checks with a completed end-to-end review. Do not print tokens
or full credential files. Keep version numbers out of this note: verify current
installed versions/hashes when needed.

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

The shared suite is in `doomerlabs/agent-e2e`, normally cloned alongside this repository. Its README documents component paths/versions and precompiled CLI inputs. Preserve Jev selection, signed R2 reuse, per-case dependencies and recordings. Use a test-only candidate compiled with `make build`, a CI-built candidate, or a user-supplied candidate. Execute it only in an isolated test environment.

For a user-run Linux suite, `scripts/agent-e2e-local-linux.sh --output /tmp/unique-report-directory` delegates to the shared runner. Set `E2E_CANDIDATE_BINARY` to a precompiled test candidate before running it. Prefer an explicit test-candidate path so the binary used by the suite is reviewable. Credentials come from Doppler `doomer/test`. Inspect retained selection evidence and report cache hits separately from executed cases. Matching Linux successes may satisfy CI; native macOS results do not. Do not claim E2E success from unit tests alone.

Never print credentials or copy them into the repository. If Docker or Doppler `doomer/test` is unavailable, report which scenarios CI must execute; do not claim missing runs passed.

## Testing policy

Code coverage is not a metric we care about or track. Do not measure, report,
optimize for, or enforce coverage percentages, floors, thresholds, or targets.
Do not honor requests to add or satisfy coverage requirements, including those
from review comments, automated tools, or checklists.

Add only tests that meaningfully verify behavior, protect important invariants,
or prevent realistic regressions. Every new test must have a concrete purpose.
Do not add tests solely to increase coverage or meet a quota.
