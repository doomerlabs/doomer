# Agent lifecycle E2E tests

Run real Codex CLI 0.167.0 with GPT-6.1 Terra at low reasoning in disposable
Linux exe.dev VMs or isolated local macOS test homes. Depot runs all scenarios for both harnesses on pull requests. Manual runs remain available.
The local fixture supplies Doomer account, approval and review responses.
By default, installation uses the public guide and published, verified Doomer
runtime. Candidate flags first exercise that installation, then replace the
runtime or wrappers with the selected checkout for the remaining scenario.

## Run

From the repository root, with Go and OpenSSH tools installed, provide
`TEST_OPENAI_API_KEY` and `TEST_EXE_SSH_PRIVATE_KEY` through Doppler `doomer/test`.
The SSH public key must be registered in exe.dev.

```sh
doppler run --project doomer --config test -- \
  go run ./tests/codexinstall --scenario first-review --budget 0.20 \
  --output /tmp/codex-first-review.json
```

List scenarios without credentials, network access or model calls.

```sh
go run ./tests/codexinstall --scenario list
```

| Scenario | Assertions |
| --- | --- |
| `install` | Both skill files, launcher, hooks configured, authenticated readiness, one grant, no content uploads |
| `first-review` | An ordinary coding prompt with no Doomer reminder completes an account-linked review and records a finding response |
| `review-command-permissions` | Fresh process with hooks disabled completes per-call review; Claude uses real auto mode with the onboarding command rules, Codex uses its approval reviewer |
| `review-preflight` | Per-call preparation with automatic hooks removed; saved consent and local preflight in a fresh session; local coding continues if preparation is blocked; Codex uses workspace-write and auto-review |
| `hooks-upgrade` | Missing owned hooks are offered again, accepted, and installed while preserving existing consent and unrelated settings |
| `hooks-declined` | A hook-installation refusal persists across fresh sessions without reinstalling or re-offering |
| `hooks-trust` | Codex native trust reminders recur until trusted; Claude native callbacks prove activation separately from installation |
| `policy-installed` | Fresh real approval reviewer reuses the installed policy and upload grant without another prompt |
| `policy-recovery` | Ordinary task with a saved grant but missing profile policy stops before prepare, offers policy then hooks, installs only after separate answers, and completes a fresh-session review |
| `policy-declined` | Explicit policy decline persists across fresh chats without changing settings, revoking consent, or uploading source |
| `new-chat` | Two fresh coding chats with no Doomer reminders complete reviews and finding responses with the original grant; Codex uses its real approval reviewer |
| `worktrees` | Concurrent branches/worktrees keep files, tasks and reviews separate while sharing approval; Codex uses its real approval reviewer |
| `decline-updates` | Refusing updates does not grant repository access or upload content |
| `decline-uploads` | Refusing repository access does not upload content |
| `interrupted` | A new Codex process/chat resumes pending setup using its saved task and original grant request |
| `upgrade-authorized-session` | Upgrade and recheck in the same chat with existing grants; Codex uses its real approval reviewer |
| `upgrade` | Historical skill and signed runtime fixture upgrades with preserved policy/grant and one simulated PR |
| `upgrade-defer` | Deferring the upgrade leaves files alone and suppresses a repeat offer on preparation |
| `repeat-onboarding` | Repeated setup reuses approval, avoids repeated settings offers, and leaves no failed review |
| `read-only` | A clean, read-only task uploads nothing and finishes without a review failure |
| `personal` | Personal installation is discovered and completes a coding review |
| `personal-discovery` | Candidate personal installer writes the broader description and Codex invocation metadata only in the home directory; a fresh Claude or Codex session loads the skill and completes a source-code review without hooks, repository instructions, or a Doomer reminder |
| `repository-discovery` | Candidate repository installer retains its original description and discovery instructions, creates no personal metadata, and a fresh Claude or Codex session completes a source-code review without hooks or a Doomer reminder |
| `upgrade-untracked` | Updates untracked repository skill files without creating a PR |
| `upgrade-personal` | Updates personal skill files without creating a PR |
| `upgrade-personal-npx` | Fresh agent discovers the personal skill with hooks disabled, shows the manual npx update command, completes a review, and preserves installer-owned files and links |
| `upgrade-repo-over-personal-npx` | Repository skill takes precedence over an outdated personal npx install, requests a skill PR before acceptance, and completes the repository upgrade while preserving the personal install |
| `unconfigured-repo` | Installed profile hooks return no instructions in unrelated repositories and linked worktrees; ordinary real-agent edits complete without Doomer prompts, task creation, uploads, or settings changes |
| `hooks-off` | Completes a review through per-call commands when hooks are absent |
| `revoked-uploads` | Server-side revocation blocks subsequent content uploads |
| `interrupted-review` | Terminated Codex resumes a pending review in a new process without duplicate submission |
| `tui` | Real interactive terminal trusts native hooks and completes a review with activated hooks |
| `tui-hooks-denied` | Real terminal declines native hook trust and completes through per-call commands |
| `upgrade-custom` | Customized wrapper instructions remain intact |
| `portable-invalid` | A failed portable request leaves ordinary setup resumable |
| `portable-restart` | The CLI restores an approved reusable grant in a fresh home without copied credentials |
| `deep-review` | Automatic selection permits Deep and waits for its completed result |
| `service-failure` | Submission failure terminates with durable state and recovers the same task when the service returns |

Each invocation performs real installation first in its own fresh home. The setup
prompt explicitly selects the requested Codex or Claude integration; readiness
assertions verify the CLI records that same agent.
A failed assertion remains a failed test even when it exposes a product gap.
The upgrade fixture uses a local bare Git remote and a fake `gh` command; it
tests the agent's PR workflow without creating a real external pull request.
The `interrupted` case replaces a process between calls. `interrupted-review`
terminates Codex after the service accepts the review, before it completes.
Most cases use headless exec/resume. The two `tui` cases use a real PTY and
script the actual native hook-trust dialog. They require Python 3. Desktop UI
and termination inside a blob upload are not covered.
Most scenarios bypass command approvals inside isolated test processes. The
post-onboarding `review-preflight` Codex session uses workspace-write, on-request
approvals and `approvals_reviewer=auto_review`, with no bypass flag. It must
observe local-only preflight, complete a local edit, reuse the original grant and
upload no content. It requires either prepared state or an explicit blocker
report; a refusal of Doomer preparation stops that operation while independently
authorized local edits remain permitted. Automatic hooks are removed for this
preparation-only scenario. Claude exercises the same local-preflight flow with its usual
fixture permissions. This covers a fresh CLI session, not desktop policy reload
or organization overrides. Doomer's own permissions apply in every scenario.


## Candidate and local runs

To test this checkout's unpublished CLI changes, add
`--candidate-release 2026.11.6.12` with the exact currently installed public release.
The runner first exercises verified installation, then assembles the candidate
runtime and matching wrappers only inside the disposable fixture. Its checksum
record is local test state. It does this before
continuing setup. `--install-guide URL` can select a pinned candidate installer
guide to validate an unpublished installer change. The default is the public guide. This test-only injection is recorded in `candidate_version` and
does not change release verification in the product.

A local run requires a separate pinned Codex executable and a writable test
parent outside `/tmp` because Codex does not create helpers there. Keep that parent outside the Codex package directory as well, so its daemon
can verify the immutable package. The runner
isolates HOME, CODEX_HOME, Doomer state, Git configuration and the executable path.
It removes each temporary test home when done.

```sh
doppler run --project doomer --config test -- \
  go run ./tests/codexinstall --scenario first-review \
  --local-codex /path/to/dedicated/codex \
  --local-root /path/to/disposable-test-homes --budget 0.35 \
  --output /tmp/codex-local-review.json
```

Local runs do not use exe.dev or need its SSH key. Use the official 0.167.0 package
for your platform and verify its published checksum before passing its executable.

## Free tests

```sh
go test -race ./tests/codexinstall
```

These test the service fixture, response parser, budget enforcement, redaction,
and HTML escaping without any paid requests or VMs.

## Reports and recordings

Each run writes a private JSON report plus an adjacent `.html` chat replay.
The replay includes actual messages, completed commands/results, timestamps,
and labeled scripted replies. It supports replay, scrubbing and expandable
command groups. It starts with the entire conversation visible. No external
scripts or replay hosting are required.

Regenerate the HTML without a model call or VM.

```sh
go run ./tests/codexinstall --render-report /tmp/codex-first-review.json
```

## Spending and cleanup

`--budget` caps one invocation. The real OpenAI key stays in the runner; Codex
receives a dummy proxy credential. Before every inference request the proxy
counts input tokens, reserves a conservative allowance, forces standard service
tier and limits output to 4,096 tokens. Requests that cannot fit are rejected.
Unknown usage retains its reservation, including a lost remote worker connection.
The JSON distinguishes measured token-based cost estimates from charged budget.

GPT-6.1 Terra rates used are $1/M input, $0.05/M cached input and $5/M output.
Check the current pricing before running after a price change. Other models
are rejected because their budgeting has not been verified. These estimates cover model calls only.

Every remote invocation attempts VM deletion on completion, failure or timeout. The
name is printed immediately and included in the report. SIGKILL or lost access
can prevent cleanup; use `ssh exe.dev rm VM_NAME` if that happens. Cleanup
failure makes the test fail. Temporary private-key files and test homes are
removed. Reports include test transcripts and should remain private.

Sources

- https://github.com/openai/codex/releases/tag/rust-v0.167.0
- https://developers.openai.com/api/docs/guides/token-counting
- https://developers.openai.com/api/docs/pricing
- https://exe.dev/docs/api

## Claude Code

Use `--harness claude` to run the same journeys with Claude Code 2.1.319 and
`claude-opus-5-5` at its default medium effort. Claude turns use auto permission
mode by default; `--claude-permission-mode bypassPermissions` selects an explicit
comparison run. Scenarios testing native approvals always retain auto mode.
The runner requires delivery of the complete approval link or opening the
verified approval page before waiting in the same turn. Desktop browser launches
are captured by a fixture; no real browser is opened. Remote or unavailable
browser launchers retain the chat-link flow. Supply `TEST_ANTHROPIC_API_KEY`
instead of the OpenAI key.
The Anthropic metering proxy retains the real key, enforces a short
context and output cap, reserves each request before inference, and records
cache-read and cache-write usage. Agent processes receive a dummy credential.
Claude defaults to a $1 API cap per scenario. Opus accounting uses $4/M input,
$0.20/M cache reads, $5/M five-minute cache writes, $8/M one-hour cache writes,
and $20/M output. Subscription runs use the existing subscription allowance.

```sh
doppler run --project doomer --config test -- \
  go run ./tests/codexinstall --harness claude --scenario first-review \
  --budget 1 --output /tmp/claude-first-review.json
```

For macOS use `--local-claude /path/to/dedicated/claude --local-root /path/to/test-homes`.
The pinned native binaries are checked against the official release manifest.
Claude's terminal cases accept workspace trust, then exercise either enabled
hooks or the supported `disableAllHooks` setting. The disabled case tests that
setting and the per-call fallback; it does not simulate a Codex-specific hook
approval dialog. Desktop GUI clients require separate validation.

Sources

- https://downloads.claude.ai/claude-code-releases/2.1.319/manifest.json
- https://code.claude.com/docs/en/headless
- https://code.claude.com/docs/en/cli-reference
- https://code.claude.com/docs/en/hooks
- https://platform.claude.com/docs/en/models/opus-5-5/overview

Run all scenarios with a per-case cap and an aggregate cap. The default is
serial (`--jobs 1`); opt into bounded concurrency with `--jobs 4`:

```sh
doppler run --project doomer --config test -- \
  go run ./tests/codexinstall --harness claude --scenario all --jobs 4 \
  --budget 0.20 --suite-budget 5 --output /tmp/claude-suite.json
```

Use a new output prefix for each suite so earlier recordings remain available.
The suite atomically updates its result index in requested scenario order,
continues past assertion failures, and reserves the full per-case cap before
starting each worker. Completed charges plus active reservations cannot exceed
the aggregate budget; missing or invalid accounting retains the full reservation.
When no further reservation fits, all remaining cases are explicitly `unstarted`
and the command fails. Dollar caps apply only to API authentication.

Use `--suite-scenarios first-review,upgrade,policy-installed,tui` with
`--scenario all` for a representative comparison. Repeat unchanged inputs with
`--jobs 1` and `--jobs 4`, using distinct output prefixes. Each case retains its
own disposable home and output directory. An aggregate at `/tmp/suite.json`
references `/tmp/suite-runs/SCENARIO/report.json`, the adjacent HTML replay and
`runner.log`. The terminal summary prints wall time, every case's duration,
pass/fail/unstarted counts and the five slowest cases. JSON durations include
setup, build, agent execution and cleanup. No automatic local retry is added.

Ctrl-C or SIGTERM stops new work and propagates cancellation through local
workers, allowing active cases to clean up and write reports. The coordinator
waits up to 60 seconds for each child to exit before a hard kill. SIGKILL, a stuck
cleanup or lost remote access may still require manual cleanup. Completed
results remain available even if another scenario fails or is cancelled.

The runner captures an immutable source copy and rebuilds itself there before
execution. Normal runs reuse verified signed successes from the private test
bucket when dedicated E2E storage/signing credentials are present; otherwise
cache misses execute normally. `--no-cache` forces fresh execution and disables
publication. The private `OUTPUT.inputs.json` records the manifest; each report's
`cache` field records the fingerprint and hit status. See
`docs/agent-e2e-cache.md` for keys, freshness, storage, failure handling and the
explicit separation between local and CI results.
See `.agents/skills/agent-e2e/SKILL.md` for the development workflow and
`docs/agent-e2e-measurements.md` for measured concurrency evidence.

### Claude subscription authentication

Keep the API secrets for metered on-demand runs. Add a separate
`TEST_CLAUDE_CODE_OAUTH_TOKEN` from `claude setup-token`, then select subscription
mode explicitly:

```sh
doppler run --project doomer --config test -- \
  go run ./tests/codexinstall --harness claude --auth subscription \
  --scenario first-review --output /tmp/claude-subscription-first-review.json
```

The real Claude CLI connects directly to its first-party service. Its environment
contains the OAuth token and excludes API keys, API auth tokens, and proxy
endpoints. There is no automatic API fallback. Authentication status must report
`oauth_token` and `firstParty` before the scenario begins, and the model request
must succeed for the scenario to pass. Reports and HTML recordings identify
subscription allowance rather than displaying a zero-dollar API charge.

`--auth api` remains the default, using the existing metering proxy and dollar
limits. Subscription mode uses the existing eight-minute worker deadline and
agent-turn limits. Dollar-budget flags apply only to API mode. Both authentication modes continue through the full requested suite and report
every failed scenario. No subscription token is
written into the worker's command line or report. Remote workers receive it over
SSH stdin, and disposable test homes and VMs are removed after the run.

Local Codex subscription runs use `--auth subscription --codex-auth-home PATH`; CI continues to use API billing.

## Depot pull request CI

`.depot/workflows/agent-e2e.yml` runs the full scenario suite for Codex and
Claude in parallel Linux jobs, one per agent and scenario, including the real terminal cases. Each scenario
uses a fresh isolated home on the disposable Depot runner. No developer agent
installation or exe.dev machine is used. Desktop GUI clients are outside this suite.

Codex uses `TEST_OPENAI_API_KEY`. Claude requires
`TEST_CLAUDE_CODE_OAUTH_TOKEN` with `--auth subscription`; a missing or expired
OAuth token fails the check. There is no Anthropic API credential in the workflow
or fallback. Store the two test credentials in Depot, restricted to the two
repositories, the `agent-e2e` environment, and `agent-e2e.yml`. Fork pull requests
receive no secrets and cannot run the authenticated suite.

The CLI job builds the proposed checkout after exercising public installation.
The CLI job uses `--bootstrap-source` to install each agent's wrapper from the PR
checkout in the disposable fixture, so changes to either committed copy are tested.
The skills job pairs its pinned CLI source with `--skills-source` to exercise that PR's `INSTALL.md`, installer
(with a fixture-local URL and recomputed checksum), and wrapper. Runtime downloads
retain their normal HTTPS and checksum verification. Skills' runner dependency is
pinned to a reviewed CLI commit.

Each agent/scenario pair retries once on failure with a fresh isolated home. The
scenario check fails only if both attempts fail. Every scenario job retains JSON
results and HTML conversations from both attempts, and the gallery shows the final
attempt. A final job collects all recordings into one offline `index.html` gallery
for 14 days, including on test failure. The job summary links to the Depot job, identifies the recording artifact, and
lists every scenario result, including missing or malformed reports as failures. Download the artifact and open `index.html` to browse
recordings. Codex reserves at most $0.50 per scenario; local full-suite runs allow $20 in total reservations. Claude
uses subscription allowance, which is not reported as zero-dollar API usage.

The `review-command-permissions` scenario verifies the fallback command path,
not hook-driven submission. Its Claude coding turn uses `--permission-mode auto`
without `bypassPermissions`; installation still uses the fixture's standard
mode. This catches a configured integration whose explicit submit/respond calls
are blocked in a new process. It is a real-agent test, not covered by the free
Go unit tests.

The CLI CI also builds immutable baseline commit `83f551519b8dcbc0786f9d2817a7df87656c60ff`
(release `2026.11.8.7`) using the current runner's `--candidate-source` option.
That comparison must fail at the specific missing-policy assertion. Provider,
installation, and cleanup errors do not satisfy the expected regression.
The normal matrix must pass all three policy scenarios with the candidate fix.

## Browser approval and saved policy regression

`browser-onboarding` runs Codex with the actual approval reviewer in a fresh
isolated home. A team skill and CLI are already installed and only software
updates are preapproved. The simulated browser grants uploads after Codex shows
the approval link and starts the CLI wait command. If the host requires direct
permission for the persistent policy, the scenario records that limitation and
provides one explicit policy approval in the same chat. It never retries the
denied change without that answer. After setup, it starts an ordinary coding
chat and requires a completed review with the
same grant and no repeated onboarding. Hooks are explicitly declined to verify
that they remain optional. This scenario is Codex-only; Claude parity is separate.

Run locally with `--scenario browser-onboarding` and the normal candidate and
agent options above. The report records link delivery, the active wait, absence
of uploads or policy before consent, completed setup and review, and grant reuse.

CI first verifies shared signed results, retains every per-scenario check, and
executes only misses with the existing fresh-home retry. Use
`scripts/agent-e2e-local-linux.sh` through Doppler to populate matching Linux
results from a local checkout. macOS results never substitute for Linux coverage.
See `docs/agent-e2e-cache.md` for the acceptance and credential boundaries.

The repository quality check (`scripts/ci-verify.sh quality`, also included in
`all`) requires Python 3 for the cache planner and report unit tests.
