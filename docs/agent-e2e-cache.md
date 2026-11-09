# Shared agent E2E results

Local runs capture their inputs, then reuse valid signed successful results or
execute missing scenarios. `--no-cache` forces execution and disables reads and
publication. CI's workflow and release gates are unchanged; wiring verified reuse
into CI is a later step.

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
deleted and executable-bit changes affect identity. Edits to the original
checkout after capture cannot alter test inputs. A changing checkout during
capture is rejected. The snapshot is removed after the child exits, including
on interruption. A private `OUTPUT.inputs.json` records the manifest and resolved
inputs for PR validation. Use a fresh output path each time.

The manifest conservatively covers repository contents and any explicitly
selected candidate, bootstrap or skills repositories. It excludes private Git
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

## Baselines and freshness

The runner resolves the public stable channel once, verifies the pinned public
installer against the guide's SHA-256, and uses a fixture-local copy of that
installer with the captured stable-channel JSON substituted. The guide receives
the derived installer checksum. Archive download HTTPS/checksum verification
remains intact. With `--skills-source`, the snapshot's installer receives the
same narrowly scoped baseline substitution. Production installers are unchanged.
A candidate release must match the resolved installation baseline for reuse.

The signed managed catalog and relevant immutable release manifests also enter
identity. The released CLI still fetches the live managed catalog, so reuse is
limited to six hours from execution start. Publication re-fetches every captured
HTTPS input and is skipped if any changed or cannot be verified. This is an
explicit freshness policy for remaining external mutability, not a promise that
all external services are immutable. Unsupported installer layouts disable reuse.

`local` and `ci` execution classes have separate fingerprints. OS and architecture
are also separate: macOS results never claim Linux coverage. There is currently
no rule accepting developer results as CI coverage. Enable such a rule only in a
later reviewed change with representative environment parity evidence.

## Signed result contract

Schema 1 stores an envelope with `key_id`, base64 `payload`, and base64
`signature`. The signature is Ed25519 over exactly the decoded payload bytes;
verification never reserializes them. The UTF-8 JSON payload records schema,
fingerprint, scenario, harness, outcome, captured inputs/environment, commit,
start/end/duration, attempts and artifact references. A result is reusable only
when its signature, trusted key, expected fingerprint, successful outcome,
execution times and all referenced artifacts verify.

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
configure the seven-day `v1/` lifecycle rule in the dashboard. For this setup the
bucket and lifecycle rule were created in the dashboard and the dedicated
credentials were provisioned with the account token. The setup token can be
revoked afterward.

The CI publisher and reader credentials are prepared in Doppler for later CI
integration; no Depot secrets or workflow are changed here. Tests run by CI can
publish with the same result contract once those credentials are explicitly
wired in. The current policy trusts approved developers and CI that hold the
dedicated signing key; stronger provenance attestations are outside this phase.

To rotate, create a new independent signing key and ID, add its public key to the
keyring, distribute the new signing secret and ID to approved publishers, and
retain the old public key for at least six hours. Then remove/revoke the old key.
Remove a compromised public key immediately: cached results using it become
misses. Rotate bucket-scoped storage tokens independently. Never substitute the
public release key or copy release credentials into the test configuration.
