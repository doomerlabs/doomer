# Managed runtime protocol 2

The repository or personal SKILL.md is a stable bootstrap. Its standard
string-valued metadata contains `doomer-protocol`, `doomer-update-policy`, and
optional `doomer-version` or `doomer-delay-hours`. Repository policy overrides
personal policy. Both repository copies must agree. The runtime parses policy;
agent prose never selects the version.

## Commands

- `doomer install --runtime --repo PATH` installs a signed runtime and matching
  Codex/Claude bootstraps. `--personal` selects user-level bootstraps.
- `doomer prepare --repo PATH --agent codex --prompt TEXT` creates an explicit
  durable task. `--session ID` binds real hook events when supported.
- `doomer prepare --task HANDLE --allow-updates` records explicit software-update
  authorization. A new policy requires new authorization.
- `doomer prepare --task HANDLE` creates or polls a persistent repository approval.
  The hosted page handles account sign-in and repository upload permission without
  a localhost callback. `doomer task login HANDLE` resumes the same flow.
- `doomer task consent HANDLE --allow` records explicit repository upload
  permission for the authenticated account, profile, service, and Git identity.
- `doomer task rollback HANDLE` rejects a bad current selection and selects the
  previous verified release for future tasks. Active tasks keep their versions.
- `doomer task pause HANDLE` and `resume` control uploads for an individual task.
- `doomer task revoke HANDLE` revokes that local repository grant.
- `doomer task submit HANDLE` uploads snapshots and journals a stable review ID
  before submission. HTTP retries and process restarts reuse the same request.
- `doomer task status HANDLE` performs one result fetch.
- `doomer task retry HANDLE` starts a new revision only after a terminal review
  failure. Network retries continue using submit/status with the original ID.
- `doomer task respond HANDLE` reads JSON responses from stdin, persists them
  before delivery, and records each server acknowledgment.
- `doomer task finish HANDLE` verifies results, response delivery, and the current
  checkout tree before reporting completion.
- `doomer hooks --agent codex|claude` installs the optional thin adapter. The user
  still controls trust. Real callbacks alone establish live activation.

Each task stores its selected release outside Git. The launcher dispatches task
commands to that immutable executable. Instructions are embedded in the same
binary. Concurrent tasks can use different versions in the same checkout.
Releases remain cached; this implementation deliberately retains old versions
rather than risking deletion of a task's runtime.

## Distribution

The protected R2 release job signs `cli/managed.json` with
`DOOMER_RELEASE_SIGNING_KEY`. The corresponding public key is embedded in the
launcher. The signed catalog names compatible releases, publication dates,
withdrawals, and SHA-256 hashes of immutable release manifests. The updater
verifies the catalog signature, expiry, manifest hash, and archive digest.
Publication uses conditional writes to preserve concurrent releases.

Stable selects the newest eligible release. Delayed requires a minimum age.
Pinned selects exactly its declared version. Manual retains the installed
selection until explicit approval. An unreachable release service can reuse a
previously verified selection. Unknown initial state requires connectivity.
A withdrawn release cannot be selected after a successful current catalog check.
Active tasks keep their version until they finish.

Every task produces a private current-state observation. Authenticated clients
also report bounded operational metadata to `/api/v3/installations/observe`,
which feeds the CLI adoption dashboard. Reporting has its own timeout and never
blocks reviews. A per-task persisted sequence rejects delayed older observations.
Review submissions
also identify their task, running CLI, and bootstrap protocol in the existing
server-side client metadata. Installing software, authenticating, granting
uploads, activating hooks, and completing a review are separate outcomes.

## Harness capabilities and limits

Per-call execution needs a shell plus durable private storage between calls.
It needs no native skill catalog, session ID, background process, or callback.
Hook mode additionally needs a real session ID and the host's trusted lifecycle
callbacks. A missing binding returns guidance to prepare; it never guesses the
most recent task. Different editing VMs have independent installation and auth.

The local task handle cannot survive destruction of the entire state volume.
Explicit server-side task recovery after complete VM loss is outside protocol 2.
The account and repository are rechecked before every review operation. Network
errors identify the failing host without printing signed URL query strings.

`prepare --portable` requests browser approval to keep the account credential,
repository grant, and approved update policy in private Git metadata. The page
discloses that environments with access to this checkout can reuse the account
access. The record lives under the Git common directory, outside committed files
and source snapshots. Subsequent preparation verifies its remote, service,
profile, file permissions, and live account before restoring local state. The
same Git storage can be mounted at a different path with an empty home. No Mac
installation is required. Revoking the task's approval also removes its portable
record. Existing local account conflicts require resolution before restoration.

For older launchers, preparation returns `portable_setup_command` as an argument
array using the exact selected runtime. Temporary-home harnesses execute it
before approving setup or editing. This enables the new operation without asking
an older launcher to parse its flag. Task history remains local to each environment.

## Verification

`go test ./internal/managed ./cmd ./internal/selfupdate` covers policy precedence,
release signatures and expiry, task isolation, lock contention, interrupted
submission recovery, response recording, revocation, native-discovery absence,
and account-authenticated per-call review completion against an isolated API.
Actual Codex/Claude/Cowork client trust and UI behavior require client smoke tests.

For isolated validation or an ephemeral harness, `DOOMER_RUNTIME_HOME` can name
an absolute root for runtime binaries. Personal bootstraps remain in the actual
user skill directories. Each task persists its selected runtime root, and hooks
persist the state location so later calls do not depend on shell exports. The default remains the user's home. Credential and task
storage continue to use `DOOMER_CONFIG_DIR` independently; credentials need not
be copied to change the runtime root.

## Consent evidence and hook feedback

Authenticated, upload-approved task results include a `consent` object with the
saved receipt path, account, repository identity, destination, approval time,
and covered data. Repository snapshots, task context, and finding responses
share the standing repository grant. Older saved grants remain usable.

Agents can cite this evidence in normal tool-approval review instead of asking
for a separate grant for each finding response. Host-level restrictions still
apply. Hook feedback contains brief state-specific guidance; full review JSON
stays available through task commands.
