# Agent onboarding E2E tests

This suite exercises the Node manager, installed Go CLI, and direct agent hooks.
It replaces the legacy managed-install lifecycle suite. There are no managed
upgrade, update-policy, portable-state, or managed-task scenarios.

Each case uses a fresh disposable home and a local review API. Codex 0.167.0
uses GPT-6.1 Terra; Claude Code 2.1.319 uses Claude Opus 5.5. API runs retain
per-case and suite budget limits. Claude CI uses subscription authentication
without API fallback. Reports retain visible agent messages and redact tokens.

## Scenarios

Both agents run every scenario:

| Scenario | Required behavior |
| --- | --- |
| install | Agent runs the real Node manager; persistent manager, native CLI, skill, direct hooks and service consent agree; no source is uploaded during setup |
| skill-first | Official skills CLI installs the candidate skill before agent-driven onboard; the resulting installation is ready |
| standalone | No hooks are selected; an explicit Go Quick review completes |
| decline-uploads | Installation succeeds without consent; later coding cannot upload source or silently grant approval |
| repeat-onboarding | Existing consent and exact hook definitions survive repeated onboarding |
| hook-repair | Repair restores direct Go hooks and preserves an unrelated hook |
| first-review | Native agent hooks submit a session/turn-identified review and record an explicit finding decision |
| new-chat | Two separate agent sessions submit distinct review groups without onboarding again |
| read-only | Native hooks on a clean repository upload nothing |
| nested-repositories | A chat above deeply nested repositories reviews both changed repositories |
| service-isolation | Consent for one API does not authorize a different API |
| uninstall | Manager removes its command, state and hooks from the disposable home |
| hooks-declined | Standalone onboarding leaves hooks unconfigured |

Review cases use a real interactive terminal to exercise native hook trust.
They wait for the agent's completion event, not an old managed-task JSON file.
API assertions require actual review submissions and explicit recorded feedback;
merely saying a review ran does not pass. The standalone case is intentionally
an explicit command flow. Setup uses disposable-home permission bypass in Codex;
it does not certify the desktop approval-reviewer UX.

## Source inputs

`onboarding-inputs.json` pins the Node repository, new skill repository, and
published official skills CLI version. `scripts/ci-agent-e2e.sh` checks out those
exact commits unless `E2E_NODE_SOURCE` or `E2E_SKILLS_SOURCE` supplies local
checkouts. Source capture includes their complete non-secret inventories and
Node's executable entrypoint. The official skills CLI and its transitive npm
dependencies are captured in a lockfile and restored with npm ci; Node/npm
versions also participate in cache identity. The Go candidate comes from the proposed Go
checkout, labelled with `baseline-release.txt`; no legacy binary is installed
or tested and no managed release catalog is consulted.

The worker copies the frozen Node package into the disposable directory,
installs its locked dependencies, and substitutes only the skill source and
locked skills CLI installation prefix. The real manager and official skills CLI
perform installation. This fixture substitution avoids requiring npm publication
or merging the new skill. It does not mock installation or modify live hooks.

## Running and validation

The user owns local CLI builds. Agents must not run the Linux helper or real E2E
commands that build a candidate CLI. CI may execute the candidate build on its
disposable runner. Unit tests can run without building a CLI:

```sh
go test -race ./tests/codexinstall -skip '^TestPrunedRuntimeBuildsRealCLIAndRunner$'
```

The skipped infrastructure test itself builds the CLI. The two CLI tests named
in root AGENTS.md also remain excluded when testing other packages.

For a user-run Linux suite, Docker and Doppler `doomer/test` provide the test
credentials without copying secrets into the repository:

```sh
doppler run --project doomer --config test -- scripts/agent-e2e-local-linux.sh
```

For CI/direct disposable runs, `E2E_REPORT_DIR` is required:

```sh
scripts/ci-agent-e2e.sh codex all
scripts/ci-agent-e2e.sh claude all
```

`--scenario list` lists only the replacement cases. The same list drives Depot,
the selector, cache planner, and recording gallery; retired cases cannot be
requested through the runner. One fresh-home retry remains in CI. Cache schema
3 separates these results from legacy suite successes. Signed successes are
reused only when sources, agent, environment and dependency inputs match.
Reports and HTML recordings stay private and retain the existing seven-day
cache and fourteen-day CI artifact lifetimes. Do not claim real-agent success
from passing unit tests alone.
