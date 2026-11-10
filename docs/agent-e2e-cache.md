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
scenario builds, bootstrap reads and fixture reads run there. Added, modified,
deleted and executable-bit changes to runtime dependencies affect identity. Edits to the original
checkout after capture cannot alter test inputs. A changing checkout during
capture is rejected. The snapshot is removed after the child exits, including
on interruption. A private `OUTPUT.inputs.json` records the manifest and resolved
inputs for PR validation. Use a fresh output path each time.

The captured inventory records repository contents and explicitly selected
candidate, bootstrap or skills repositories. The runnable snapshot and cache key
use compiler-reported sources and embedded files plus declared runtime fixtures;
explicit installer/skills repositories retain their full inventory. It excludes private Git
metadata, credentials (`.env*`, `.aws`, `auth.json`, `credentials*`, private key
files), dependency/build caches, root binaries, report directories and run
outputs. Symlinks and submodules fail capture until explicitly supported, rather
than reading external paths. Nonempty `GOFLAGS` is rejected to prevent overlays
outside the captured tree.

A versioned SHA-256 identity covers the sorted path/byte-hash/mode manifest,
harness, model, reasoning, auth mode, per-run budget, toolchain and relevant build
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

Audited scenario fixture bodies have narrower scopes. For example, a body change
in `upgrade.go` invalidates the six upgrade scenarios, while a cached `install`
result remains usable. `hook_setup.go` affects the hook upgrade/decline/trust
scenarios. Imports, signatures, global declarations, initialization, shared runner
code, the CLI and dependencies remain shared. Any unexpected caller or opaque
reflection makes a scoped fixture shared again. A pinned audit of shared runner
control flow disables narrow scopes if that code changes; update that audit only
after reviewing the dependency boundaries. New or uncertain dependencies
therefore cause reruns rather than optimistic reuse.

| Change | Invalidation |
| --- | --- |
| Ordinary documentation or development instructions | No agent rerun |
| Audited scenario fixture body | Its declared dependent scenarios |
| Fixture signature, imports, globals or unexpected caller | Shared rerun |
| Shared CLI/runner source, toolchain, baseline or settings | Shared rerun |
| New compiled source or embedded resource | Included automatically |

The full inventory and selected runtime files are recorded in
`OUTPUT.inputs.json`; `scenario_files` records narrower fixture scopes. Cached
reports carry the original execution commit/time and origin environment, alongside
the cache-hit marker for the current run. Schema 1 results are not reused after
this dependency-contract change. The existing `v1/` storage prefix remains under
the same seven-day retention rule.

## Baselines and freshness

When a candidate release is supplied, the runner constructs the installation
channel from that immutable release manifest; otherwise it resolves the public
stable channel once. It verifies the pinned public
installer against the guide's SHA-256, and uses a fixture-local copy of that
installer with the captured stable-channel JSON substituted. The guide receives
the derived installer checksum. Archive download HTTPS/checksum verification
remains intact. With `--skills-source`, the snapshot's installer receives the
same narrowly scoped baseline substitution. Production installers are unchanged.
The selected candidate release remains the installation baseline even if a new
stable release is published while jobs are queued.

The signed managed catalog and relevant immutable release manifests also enter
identity. The released CLI still fetches the live managed catalog, so reuse is
limited to six hours from execution start. Publication re-fetches every captured
HTTPS input and is skipped if any changed or cannot be verified. This is an
explicit freshness policy for remaining external mutability, not a promise that
all external services are immutable. Unsupported installer layouts disable reuse.

`local` and `ci` execution classes remain explicit provenance. Linux results can
be accepted across those two classes only with every other fingerprinted input
matching; OS and architecture remain separate. macOS results never claim Linux
coverage. Use the local Linux command below to populate CI-compatible results.

## Signed result contract

Schema 2 stores an envelope with `key_id`, base64 `payload`, and base64
`signature`. The signature is Ed25519 over exactly the decoded payload bytes;
verification never reserializes them. The UTF-8 JSON payload records schema,
fingerprint, scenario, harness, outcome, captured inputs/environment, commit,
start/end/duration, attempts and artifact references. A result is reusable only
when its signature, trusted key, expected fingerprint, successful outcome,
execution times and all referenced artifacts verify. The historical regression
uses a separate fingerprinted assertion kind: `expect-policy-regression=true`.
For that kind, a successful outcome means the exact missing-policy failure was
observed and the local fixture was cleaned. Its signed envelope still records
`outcome: passed`; the original report retains `passed: false`. The flag is part
of the fingerprint, so this witness cannot satisfy an ordinary lifecycle check.
All other failures stay under `v1/failures/` and cannot replace a success.

Object layout:

- `v1/artifacts/SHA256.json` — original private scenario JSON.
- `v1/artifacts/SHA256.html` — original private conversation replay.
- `v1/results/FINGERPRINT.json` — discoverable signed success.
- `v1/failures/FINGERPRINT/TIMESTAMP.json` — signed diagnostic failure.

Artifacts upload before the result. Missing/truncated/tampered artifacts, unknown
keys, malformed payloads and expired results are misses. Failures never replace
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
retain the old public key for at least six hours. Then remove/revoke the old key.
Remove a compromised public key immediately: cached results using it become
misses. Rotate bucket-scoped storage tokens independently. Never substitute the
public release key or copy release credentials into the test configuration.

## CI reuse and local Linux validation

The E2E plan job verifies every required signed result and both report artifacts
before classifying a scenario as cached. Cached matrix jobs retain their existing
check names and complete without an agent process. Missing, expired, malformed,
or unavailable results run through the existing fresh-home retry path and publish
new signed results. The combined gallery includes both cached and executed
recordings, labels their origin, and charges zero new provider spend for hits.
The released-CLI regression check can reuse only a separately signed witness of
the exact expected missing-policy failure with successful cleanup.

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

This builds an Ubuntu 24.04 / linux-amd64 container with Go 1.26.10, runs both
pinned harnesses and the historical regression with bounded concurrency, and
publishes successful results. It uses the same execution script as CI, including
a $0.50 per-case API reservation and subscription-only Claude authentication.
Reports default to `/tmp/doomer-agent-e2e-linux` (`E2E_LOCAL_REPORTS` overrides it).
The host report directory is owner-only, and the container runs as the calling
UID/GID. Its private temporary home is removed on exit. Go caches are kept under
`${XDG_CACHE_HOME:-$HOME/.cache}/doomer-agent-e2e`.
`E2E_SUITE_SCENARIOS` can select a comma-separated subset for parity experiments;
omit it when priming the full required suite. Each failed local scenario retries
once in a fresh home. A partial local run never satisfies missing CI checks.

`--cache-only` verifies and downloads shared results without making agent calls.
It fails on a miss, allowing the plan to choose the ordinary execution path.
The plan's `cache-plan.json` records required and verified counts, the separate
regression result, and elapsed verification time.

CI and the local Linux helper select the explicit published baseline in
`tests/codexinstall/baseline-release.txt`. The captured installer pins its
runtime install to that version. Candidate builds use a private Go overlay that
limits release selection to that baseline and older releases, preserving stable
policy and normal signed-catalog verification. The captured source itself is
unchanged. Cache identity includes the signed catalog's eligible releases;
a newly published later release does not invalidate a run, while changes to an
eligible release do. Advancing the baseline requires changing the checked-in
version and rerunning the suite.
