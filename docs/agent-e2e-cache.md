# Shared agent E2E results

Local runs capture their inputs, then reuse valid signed successful results or
execute missing scenarios. `--no-cache` forces execution and disables reads and
publication. CI verifies shared successes before running misses; release gates are unchanged.

## Running

Use the commands in `tests/codexinstall/README.md`, with AWS CLI v2 available and
Doppler `doomer/test` supplying the dedicated `E2E_R2_*` storage settings and
`E2E_CACHE_*` signing/verification settings. The default bucket is exclusively
`doomer-agent-e2e`; the runner rejects other names. It does not use release-bucket
credentials or the release signing key. Missing configuration and storage or
verification errors print an explicit cache miss and execute the scenario.

Every normal invocation copies tracked and nonignored untracked inputs into a
private temporary snapshot. The runner itself is rebuilt from this snapshot;
candidate builds, Node installation and fixture reads run there. Added, modified,
deleted and executable-bit changes to runtime dependencies affect identity. Edits to the original
checkout after capture cannot alter test inputs. A changing checkout during
capture is rejected. The snapshot is removed after the child exits, including
on interruption. A private `OUTPUT.inputs.json` records the manifest and resolved
inputs for PR validation. Use a fresh output path each time.

The captured inventory records repository contents and explicitly selected
candidate Go, Node manager and new-skill repositories. The runnable snapshot and cache key
use compiler-reported sources and embedded files plus declared runtime fixtures;
explicit Node/skills repositories retain their full inventory, including the Node entrypoint. The official skills CLI dependency lockfile is captured too. It excludes private Git
metadata, credentials (`.env*`, `.aws`, `auth.json`, `credentials*`, private key
files), dependency/build caches, root binaries, report directories and run
outputs. Symlinks and submodules fail capture until explicitly supported, rather
than reading external paths. Nonempty `GOFLAGS` is rejected to prevent overlays
outside the captured tree.

A versioned SHA-256 identity covers the scenario's selected runtime manifest
(sorted paths, byte hashes and modes), including shared inputs and its relevant
fixture bodies. The full captured inventory is provenance, not the cache key.
The identity also covers harness, model, reasoning, auth mode, per-run budget, toolchain and relevant build
settings, pinned local agent package bytes, OS, architecture, execution class,
scenario and external input identities. Commit SHA is provenance only. Worker
count, suite-wide budget, temporary paths, report destination and credential
values are excluded. Identical contents therefore survive commits and rebases.

## Incremental scenario dependencies

Each scenario hashes only its runtime dependencies. Documentation, `AGENTS.md`,
unit tests and unrelated tooling do not invalidate agent runs unless compiled or
embedded into the tested program. The runner asks Go for compiled sources and
embedded resources, retains declared runtime fixture inputs, and removes unused
files from the execution snapshot. An undeclared runtime file read therefore
fails instead of silently depending on an unhashed file.

The runner automatically follows function references from executable roots and
scenario dispatch. It hashes the function bodies reachable by each scenario,
including transitive helper calls. Unrelated bodies and unreachable dispatch
branches are omitted from that scenario's projection; imports, declarations,
global initialization and compiler directives stay included. For example,
changing `reviewPreflight` invalidates `review-preflight`, while an unchanged
`install` result remains reusable. Adding a new helper call adds that dependency
automatically. No hand-maintained file-to-test list is used for this selection.

Function values that can escape direct calls are shared. Scenario mutation,
unsafe/reflection paths, native code, and unsupported identity flow disable
projection. Unknown scenario names retain the full inputs. The production CLI
remains shared because an agent can choose CLI commands dynamically; these
projections narrow the E2E runner and fixture code, not arbitrary CLI paths.

`e2e-extra-dependencies.yml` adds whole-file dependencies by file/glob and stable
test ID (`codex/review-preflight`, `claude/*`, or `*`). An explicit dependency
always overrides an automatic body exclusion. Invalid or unmatched rules fail
snapshot preparation. It cannot remove automatically discovered dependencies.

| Change | Invalidation |
| --- | --- |
| Ordinary documentation or development instructions | No agent rerun |
| Scenario-specific runner/helper body | Automatically discovered dependent scenarios |
| New helper call | Its callers acquire the new dependency |
| Imports, declarations, globals or shared helper body | Shared rerun |
| CLI source, toolchain, baseline or settings | Shared rerun |
| Explicit extra dependency | Every test selected by its rule |
| Uncertain scenario identity or unsafe runtime behavior | Full runner inputs |

The full inventory and selected runtime files are recorded in
`OUTPUT.inputs.json`; `scenario_hashes` contains the per-scenario runner
projections. `dependency_analysis.scenario_projection` says whether they are
enabled or the runner used conservative inputs. The general cross-package
analysis remains diagnostic, with its full graph in `OUTPUT.dependencies.json`.
Cached reports preserve the original execution commit/time and origin
environment, alongside the cache-hit marker for the current run. The existing
`v1/` storage prefix retains its seven-day retention rule.

## Input pins and freshness

The replacement onboarding suite uses `tests/codexinstall/onboarding-inputs.json`
for its Node manager revision, new `doomer` skill revision, and official skills
CLI version. The Node manager and skill sources are copied into the frozen
snapshot. The official skills CLI's dependency lockfile is generated before
execution; its contents and the Node/npm versions enter cache identity.
The worker restores dependencies with `npm ci` and runs the actual manager and
skills CLI. Only the external skill-source and installation-prefix arguments
are substituted in the private manager copy.

The Go candidate is built from the proposed source. `baseline-release.txt` is
only the version label for that disposable candidate. The suite does not install
a historical runtime, consult a managed release catalog, or exercise upgrades.
Publication rechecks captured HTTPS package metadata; changed or unavailable
inputs prevent publication. Cache fingerprint schema 3 prevents legacy suite
successes from satisfying the new onboarding scenarios.

`local` and `ci` execution classes remain explicit provenance. Linux results can
be accepted across those two classes only with every other fingerprinted input
matching; OS and architecture remain separate. macOS results never claim Linux
coverage. Use the local Linux command below to populate CI-compatible results.

## Signed result contract

Schema 3 stores an envelope with `key_id`, base64 `payload`, and base64
`signature`. The signature is Ed25519 over exactly the decoded payload bytes;
verification never reserializes them. The UTF-8 JSON payload records schema,
fingerprint, scenario, harness, outcome, captured inputs/environment, commit,
start/end/duration, attempts and artifact references. A result is reusable only
when its signature, trusted key, expected fingerprint, successful outcome,
execution times and all referenced artifacts verify. Only an actual passing
scenario is eligible; there is no expected legacy-policy-failure exception.
All other failures stay under `v1/failures/` and cannot replace a success.

Object layout:

- `v1/artifacts/SHA256.json` — original private scenario JSON.
- `v1/artifacts/SHA256.html` — original private conversation replay.
- `v1/results/FINGERPRINT.json` — discoverable signed success.
- `v1/failures/FINGERPRINT/TIMESTAMP.json` — signed diagnostic failure.

Artifacts upload before the result. Missing/truncated/tampered artifacts, unknown
keys and malformed payloads are misses. Failures never replace
a discoverable success. Downloaded reports retain original usage and a cache-hit
provenance field; suite budget charges are zero for reused scenarios because no
new provider calls occurred. Local JSON and HTML are written only after both
artifacts have verified. Private reports are not committed or attached to PRs.

## Provisioning and rotation

Run `scripts/provision-agent-e2e-cache.py` through Doppler `doomer/test` with
`E2E_CLOUDFLARE_ACCOUNT_ID` and `E2E_CLOUDFLARE_API_TOKEN`. An account token needs
R2 storage write and account-token write permissions; a user token needs R2
storage write and user-token write permissions. The script creates or verifies the private
bucket, preserves unrelated lifecycle rules, and when administrative access is
available adds seven-day retention under
`v1/`, creates separate bucket-scoped local publisher, CI publisher and reader
credentials, generates an independent Ed25519 key, and saves secrets through
Doppler stdin. Only public verification keys enter the repository. No public
bucket domain is enabled. A token scoped only to existing bucket objects can
provision the narrower credentials without bucket administration; in that case
configure the seven-day `v1/` lifecycle rule in the dashboard. Before enabling
publication, reopen the bucket's lifecycle settings and verify that the rule is
enabled, matches prefix `v1/`, and deletes objects after seven days. Do not enable
publisher credentials if that verification is unavailable. Object-scoped runtime
credentials cannot inspect or alter lifecycle settings; this verification is a
provisioning prerequisite. For this setup the
bucket and lifecycle rule were created in the dashboard and the dedicated
credentials were provisioned with the account token. The setup token can be
revoked afterward.

The CI publisher and reader credentials are scoped in Depot to this repository,
the `agent-e2e` environment and `agent-e2e.yml`. Verification receives read-only
credentials; fallback execution receives publishing and signing credentials. The current policy trusts approved developers and CI that hold the
dedicated signing key; stronger provenance attestations are outside this phase.

To rotate, create a new independent signing key and ID, add its public key to the
keyring, distribute the new signing secret and ID to approved publishers, and
retain the old public key until results signed with it have left the bucket,
normally seven days after its last publication. Then remove/revoke the old key.
Remove a compromised public key immediately: cached results using it become
misses. Rotate bucket-scoped storage tokens independently. Never substitute the
public release key or copy release credentials into the test configuration.

## CI reuse and local Linux validation

The E2E plan job verifies every required signed result and both report artifacts
before classifying a scenario as cached. Cached matrix jobs retain their existing
check names and complete without an agent process. Missing, malformed,
or unavailable results run through the existing fresh-home retry path and publish
new signed results. The combined gallery includes both cached and executed
recordings, labels their origin, and charges zero new provider spend for hits.
Linux local and CI results are mutually acceptable only when OS, architecture,
source, pinned agent package, toolchain, model, authentication and all other
fingerprinted settings match. macOS and Windows results do not satisfy Linux
checks. CI uses read-only bucket credentials for its verification plan and separate
publisher credentials only in fallback jobs. The matrix retains a fixed eight-job limit for both lightweight cached
checks and provider-backed fallback execution. Release gates and the required scenario set are unchanged.

To populate matching Linux results from the current local checkout:

```sh
doppler run --project doomer --config test -- scripts/agent-e2e-local-linux.sh
```

This builds an Ubuntu 24.04 / linux-amd64 container with the pinned Go toolchain, runs both
pinned harnesses with bounded concurrency, and
publishes successful results. It uses the same execution script as CI, including
a $0.50 per-case API reservation and subscription-only Claude authentication.
Each invocation creates and prints a private `run.XXXXXX` report directory under
`/tmp/doomer-agent-e2e-linux` (`E2E_LOCAL_REPORTS` overrides the parent directory).
Concurrent and repeated invocations retain independent selections and reports.
The host report directory is owner-only, and the container runs as the calling
UID/GID. Its private temporary home is removed on exit. Go caches are kept under
`${XDG_CACHE_HOME:-$HOME/.cache}/doomer-agent-e2e`. The same private directory
also caches pinned agent downloads, rechecking their published SHA-256 before
each reuse. Corrupt or incomplete downloads are never accepted.
`E2E_SUITE_SCENARIOS` can select a comma-separated subset for parity experiments;
omit it when priming the full required suite. Each failed local scenario retries
once in a fresh home. A partial local run never satisfies missing CI checks.

`--cache-only` verifies and downloads shared results without making agent calls.
It fails on a miss, allowing the plan to choose the ordinary execution path.
The plan's `cache-plan.json` records required and verified counts, elapsed verification time, and the selected scenarios.

CI and the local Linux helper use the same pinned source inputs and candidate
version label. The resulting source and dependency identities, rather than a
published legacy runtime or policy catalog, control reuse. Advance a source
pin when changing the corresponding repository, then validate the resulting
new fingerprint before relying on its cache entry.
