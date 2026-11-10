---
name: agent-e2e
description: Run and diagnose Doomer's real-agent lifecycle E2E tests when changing installation, onboarding, review, upgrade, policy, hooks, or the agent harness.
---

# Agent lifecycle E2E

Read `tests/codexinstall/README.md` for prerequisites, pinned agent versions,
authentication modes and scenario assertions. Run fast relevant tests first,
including `go test -race ./tests/codexinstall` for runner changes.

Start representative E2E once relevant implementation changes stabilize, while
reviewing the diff and preparing the PR. Include ordinary review, upgrade, policy
and terminal coverage when changing the runner. Do not require paid E2E before
every checkpoint commit. Before marking a PR ready, check that the tested source
matches the final proposed contents and explain any missing coverage.

Use dedicated pinned agent executables and a writable disposable-home parent
outside `/tmp` and outside the agent package directory. Never use the developer's
installed Doomer as the candidate. Use `--candidate-release VERSION` with the
currently published version and `--bootstrap-source .` to exercise this checkout.
For local tests, `--candidate-source PATH` selects a different CLI checkout.

Example from the repository root, replacing the executable, home parent and
published version with verified local values:

```sh
doppler run --project doomer --config test -- \
  go run ./tests/codexinstall --scenario all --jobs 4 \
  --suite-scenarios first-review,upgrade,policy-installed,tui \
  --local-codex /path/to/dedicated/codex \
  --local-root /path/to/disposable-homes \
  --candidate-release VERSION --bootstrap-source . \
  --budget 0.50 --suite-budget 2 --output /tmp/e2e-review.json
```

Doppler `doomer/test` supplies `TEST_OPENAI_API_KEY` for Codex API tests or
`TEST_ANTHROPIC_API_KEY` for Claude API tests. Claude subscription tests use
`--harness claude --auth subscription --local-claude PATH` and
`TEST_CLAUDE_CODE_OAUTH_TOKEN`; Codex subscription uses `--auth subscription
--codex-auth-home PATH`. Keep secrets and private recordings out of Git and PR
attachments. Remote runs additionally need `TEST_EXE_SSH_PRIVATE_KEY`.

`--jobs 1` is the default serial mode; `--jobs 4` is an explicit experiment.
Omit `--suite-scenarios` to run the full suite. API scheduling reserves each active
case's full `--budget`; `--suite-budget` includes completed charges and active
reservations. If no remaining case fits, the aggregate reports it as unstarted
and exits unsuccessfully. Choose a cap that can cover the requested suite.
Subscription mode has no dollar accounting or automatic API fallback.

Each scenario runs in its own fresh home. For `/tmp/e2e-review.json`, results are
in `/tmp/e2e-review-runs/SCENARIO/`: `report.json`, `report.json.html`, and
`runner.log`. The aggregate is atomically updated in requested scenario order,
including failures and unstarted work. Always use a new output prefix. Ctrl-C
and SIGTERM stop scheduling, cancel active cases, allow cleanup and retain
completed results. SIGKILL cannot guarantee cleanup; inspect reported leftovers.
Failures continue through other scenarios; there are no automatic local retries.
Inspect each failure's JSON, replay and runner log before deciding to rerun.

Read `docs/agent-e2e-cache.md` for the snapshot, fingerprint, signing and storage
contract. The runner captures source before execution and rebuilds itself from
that snapshot. `OUTPUT.inputs.json` contains the tested manifest; source edits
after capture require a new invocation before marking the PR ready. Normal runs
look up signed successes, verify their reports, execute misses and publish as
each scenario finishes. Missing storage or invalid results are explicit misses.
`--no-cache` bypasses lookup and publication, retaining snapshot isolation.

AWS CLI v2 plus dedicated `E2E_R2_*` and `E2E_CACHE_*` settings from Doppler
`doomer/test` enable shared reuse. Never use release credentials or signing keys.
Retained private reports expire from storage after seven days; reuse is limited
to six hours and requires matching external inputs. OS and architecture remain separate identities. For CI-compatible results, use
`scripts/agent-e2e-local-linux.sh` through Doppler; the Linux local/CI acceptance
rule still requires every other fingerprinted input to match.

Record the result fingerprint from each report's `cache` field, manifest/report
locations, cache hits versus executed scenarios, harness/model/auth mode,
baseline, worker count, wall time and provider errors in PR validation notes.
Cached reports retain historical usage but add cache-hit provenance; suite
charges are zero for a hit. CI retains its existing retries and release gates.

Incremental reuse is conservative. If the shared-runner scope audit changes,
review the affected calls and scenario boundaries before updating its digest;
never update the digest merely to silence a test failure. Uncertain dependencies
remain shared and rerun.
