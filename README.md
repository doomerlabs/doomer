# Doomer CLI

<!-- Temporary watch test; safe to remove. -->

A Cobra/Viper client for the Doomer SaaS. It supports connection
profiles, login, logout, packaging and publishing private adversaries, and submitting
Git worktrees for hosted review with `doomer review`. Local `run` execution is not implemented.

## Build

Requires Go 1.26.9 or newer.

The Nix development shell supplies Go 1.26.9, Node 22, and the build and CI
tools. With Nix and direnv installed, run `direnv allow` in this checkout to
activate it. You can also enter the shell with `nix develop`.

```sh
go build -o bin/doomer .
./bin/doomer --help
```

## Watch repositories while you code

The CLI uses the same workspace review API as the Mac app. Without a login,
it runs free anonymous Look Closer reviews and returns an unlisted review link.
A signed-in profile can also run Deep review. The Mac app is not required.

```sh
go build -o bin/doomer .
./bin/doomer login                  # optional: enables authenticated reviews
./bin/doomer watch
```

For a coding agent, use `watch start` to approve the current repository and
start or reuse a detached watcher. The CLI opens a local consent page on macOS
and Linux desktops. Anonymous users must approve uploads too; invoking a command,
installing hooks, or having an anonymous identity is not upload permission.

```sh
./bin/doomer watch start --browser  # human approves in browser; no terminal input needed
./bin/doomer watch status --json    # consent, installed hooks, pause state and watcher health
./bin/doomer watch stop             # pause this profile, preserving upload approval
./bin/doomer watch start --resume   # explicitly resume after a pause
./bin/doomer watch revoke .         # revoke this repo and its linked worktrees
./bin/doomer watch --no-browser     # foreground watcher with terminal approval, including SSH
```

With no directory argument, new setup is limited to the current Git repository
and its linked worktrees. Adding a different repository asks again. Approved
repositories do not ask again unless approval is revoked or the profile's
identity changes. Older anonymous configurations that treated invocation as
approval require a fresh human decision. Existing signed-in approvals are retained.
Base and current source snapshots, including files outside the diff, the task
prompt behind an agent edit, and agent review responses may be uploaded.

Browser consent is served on a one-time loopback listener by the CLI itself.
It shows the exact repository scope and API destination; rendering it sends no
code or credentials to the service. Approval expires after five minutes and
cancelling leaves existing settings alone. The listener validates the nonce and
host, and accepts only one decision. On a remote server use `--no-browser` in an
interactive terminal. Piped text never counts as terminal approval. `--yes` remains
available for a human deliberately choosing unattended setup; a coding agent must
never pass it on the user's behalf.

In Codex, open `/hooks` and review/trust the new definitions. Installing hooks
does not grant that trust. Setup preserves unrelated hooks; repeated startup
reuses installed definitions. `status --json` deliberately reports hook trust as
`user_managed`, since installed hooks alone do not prove they have been trusted.

`watch start` maintains one watcher per API endpoint/profile, with private local
logs. It reuses a running watcher or restarts it after new repository approval.
The child has detached input/output and survives the launching agent command.
It does not install an OS service or restart after reboot; a later `watch start`
can restore it. `watch stop` persists a pause, which ordinary `watch start` refuses
to undo without `--resume`. Revocation cancels the running watcher; restart it
from a remaining approved repository if needed. A running foreground watcher can
be reused only for repositories it already covers.

`watch` prints each repository it watches, detected changes, uploads, and initial
assessment progress. Starting the watcher never uploads existing changes. Only
an attributable coding-agent edit during this watcher session starts a review;
manual edits and prompts without edits do not. Existing dirty files remain part
of the reviewed snapshot once an agent edit triggers it. Direct/manual review is
not part of this watch workflow.

With no directories, a foreground watcher includes the current Git checkout
if approved for this profile. Legacy all-repository approvals also allow discovery
through coding-agent hooks. New setup outside a Git checkout requires an explicit
repository path. Use `doomer watch .` to watch only the current
checkout for this run, or `doomer watch --debug` for scan, discovery, and review-status details.
Diagnostics do not print source contents, prompts, review responses, or credentials.

Registered Git worktrees are discovered at startup and every five seconds, including
worktrees created while watch is running. Discovery alone does not activate a linked
checkout's filesystem watches or upload its contents. A coding-agent edit activates it.
Automatically discovered checkouts become inactive after 14 days without agent edit
activity; a later agent edit reactivates them. There is no fixed checkout-count limit.
Each checkout keeps its own review and conversation identity. Upload approval still
applies independently to each checkout.

Manage selections independently of upload approval:

```sh
./bin/doomer watch ls
./bin/doomer watch ls --worktrees
./bin/doomer watch add /path/to/repo
./bin/doomer watch rm /path/to/repo
```

`ls` shows one row per Git repository. `ls --worktrees` expands each repository
into a tree of branch names, checkout paths, and live watch states. Checkouts show
Watching only when the current watcher session has activated them; eligible
checkouts show Not running when no watcher is running.

`add` enables the repository and its linked worktrees; the explicitly added checkout
stays active until removed, while automatic linked checkouts still age out.
`rm` stops all checkouts of that repository and blocks hook-driven rediscovery,
including new linked worktrees, until the repository is added again.
Selections, removals, and upload approval are scoped to the selected API and profile.
Use `watch uninstall` to remove hooks and revoke upload approval.

The watcher uses filesystem notifications with a 750 ms debounce. A one-minute
fallback scan catches missed events; `--interval` adjusts that fallback. Git-ignored
files and directories do not trigger reviews.

Workspace snapshots have a 1 GiB content safety ceiling (1.5 GiB encoded locally),
a 20 MiB per-file limit, and a separate 16 MiB diff limit. These are separate from
the smaller legacy embedded-upload limits below. Uploads still send only missing
content blobs; unchanged files are reused.

Start with `watch start` (or keep foreground `watch` running), then edit the approved repository
in Codex. After files settle, Doomer uploads a bounded snapshot and starts the
hosted workspace review. The synchronous hook waits at most 30 seconds for the
initial assessment and otherwise releases the agent silently. For an entitled signed-in profile, any deep review continues on the service;
it is never waited on by the hook. Anonymous watching runs only free Look Closer. New leads include a
link to the web review and instructions to investigate within your task's scope.
Outcome lines in the agent's final reply are captured locally and uploaded by the
running watcher. Missing agent outcomes are never invented.

After an edit, the hook asks the agent to submit up to five concrete review
hypotheses through a local CLI command. That command only emits a tool-result envelope; the
trusted hook saves the questions, and the watcher sends them with the edit for
SubQ assessment. Requests are bound to that conversation and edit; stale replies
are rejected. If no questions arrive within 20 seconds, the watcher uses its
independent System1 checks instead. The agent can continue if submission fails.


After the first upload, the watcher reuses stored snapshots and sends only changed
file entries, deletions, and file contents the service is missing. `--debug` shows
manifest, blob, finalization, and review-submission timings. Cached snapshot
references are isolated by destination, profile, login, and checkout.

```sh
./bin/doomer --profile dev watch add /path/to/repo --agent both
./bin/doomer --profile dev watch /path/to/repo
./bin/doomer --profile dev watch ls
./bin/doomer --profile dev watch uninstall
```

`--agent` accepts `codex` (default), `claude`, or `both`. Hooks and watched paths are
isolated by API endpoint and login profile. Changing the login requires renewed
setup approval. `watch uninstall` removes this profile's CLI hooks and revokes
its consent; a running watcher stops when it detects revocation. Ctrl+C stops a
watcher without uninstalling it. Existing Mac-app hooks are preserved: stop the
Mac app while testing this CLI so both integrations do not review the same edits.

For a directory list, pass `watch --config watch.json` containing:

```json
{"directories":["/path/to/repo","/path/to/another-repo"]}
```

First-time setup obtains approval for the listed repositories; expanding an existing
restricted selection requires additional approval. No recursive
repository discovery is performed. `--base` selects an explicit local baseline.
The triggering user request is included as review context; it is not attributed
to every changed line. This version does not yet carry prompt-to-line provenance or associate
commits with PR reviews. Treat this as a local test build, not an unattended
system service. Authentication/network failures remain visible in the watcher
terminal or private background log; they do not block the coding agent.

## Finding evidence

List project findings, then use an ID from the list or a Doomer PR comment to retrieve its full evidence as markdown:

```sh
doomer finding ls --project my-project
doomer finding get a1b2c3d --project my-project
doomer --profile work finding get <finding-id>
```

`ls` shows the newest 50 stored findings. Use `--limit` (1–200) and `--offset` to page through older findings.

This uses the selected profile's existing login and API endpoint. Short IDs are
unique within a project and use `--project` or the profile's selected default
project. The server checks your current access. Full IDs from older comments
remain supported without a project selector. Both forms work whether the team
allows secret links or requires CLI-only access, including retained evidence
after a PR closes.

The SaaS must support the finding-detail endpoint and enable the project's
`review_finding_details` feature flag. A missing finding, unavailable project
access, or disabled flag returns an error without printing private response data.
This command reads evidence only; it does not modify code or GitHub comments.

## Profiles

```sh
doomer profile add work --endpoint https://doomer.ai/api
doomer profile use work
doomer profile ls
doomer --profile work login
doomer --profile work logout
doomer profile rename work company
doomer profile rm company
```

The default profile is `default`. A profile can also be used directly with
`--profile NAME` without saving settings first. Tokens are isolated by both
profile name and API service. Profile names are case insensitive and accept
letters, digits, hyphens, and underscores.

Connection precedence is `--api-url`, `DOOMER_API_URL`, top-level `api-url` in
settings, the selected profile's endpoint, then
`https://doomer.ai/api`. `DOOMER_REGISTRY_HOST` overrides the registry host
stored with login credentials (default: `registry.doomer.ai`).
Profile selection is `--profile`, `DOOMER_PROFILE`,
the saved selected profile, then `default`.

Renaming a profile preserves its endpoint and credentials and updates the saved
selection. Removing a profile deletes its local credentials for every endpoint
without revoking tokens remotely. Removing the selected profile selects
`default`. The implicit `default` profile remains available after removal.

Settings live in `settings.yaml` and credentials in `config.json` under the
platform's user configuration directory for `doomer`. `DOOMER_CONFIG_DIR`
overrides that directory for isolated installations or tests. Settings never
contain tokens. The credential format and platform path are compatible with
previous installations; the legacy `~/.adversary/config.json` fallback is
retained when using the default directory.

Profiles separate accounts and API connections, including multiple accounts on
the same service. Login authenticates the selected account without choosing a
project. Each profile can select a default project for its API endpoint with
`doomer project use <slug>`. An explicit `--project` overrides that selection.

## Projects

```sh
doomer project ls
doomer --profile work project ls
doomer project use my-team
```

Lists the selected account's projects with `SLUG` and `NAME` columns, sorted by
slug, with `*` beside the selected default. `project list` is an alias.
When the account has only one project and no saved selection, that project is
used automatically. With multiple projects, select one with `project use`.
Service-account and CI tokens list their scoped project. Logging out clears the
selected project for that login; other profiles and endpoints are unchanged.

## Authentication

```sh
# Browser login with PKCE and a loopback callback
doomer --profile work login --name "My laptop"

# Headless device login or short-lived automation login
doomer --profile work login --device
doomer --profile work login --ci

# Password login (otherwise prompts with hidden terminal input)
printf '%s\n' "$DOOMER_PASSWORD" | doomer --profile work login \
  --email-address user@example.com --password-stdin

# Import an existing service-account or CI token
printf '%s\n' "$DOOMER_TOKEN" | doomer --profile work login --token-stdin

# Revoke remotely, then remove local credentials
doomer --profile work logout

# Remove credentials without contacting the SaaS
doomer --profile work logout --local-only
```

`--registry-namespace` remains accepted with `--token-stdin` for compatibility.
If the selected profile already has an unexpired login, `login` creates and
selects a new profile (for example, `default-2`) and preserves the existing
credentials. The new profile keeps the same API endpoint. Use `login --replace`
to deliberately replace the selected login, or `--profile NAME login` to use a
named profile. Expired logins are refreshed in place. Failed authentication does
not create or select a profile.
If revocation fails, logout preserves local credentials for retry. Credential
writes are atomic, locked, and restricted to the current user on Unix. Logout
uses compare-and-swap to avoid deleting a concurrently replaced token. API
endpoints require HTTPS, with loopback HTTP allowed for development.

## Review your worktree

```sh
# Review the repository containing the current directory:
doomer review

# Select a profile and another worktree:
doomer --profile dev review --path ../feature-worktree

# Choose a baseline explicitly:
doomer review --base origin/main --format json
```

There is no project selector for `review`. SaaS uses the repository's Git remote
to find a unique project accessible to the logged-in account. With no match or
multiple matches, account-level settings apply. A saved project is ignored for
reviews; other commands retain their project selection.
`--path` defaults to the current directory; a subdirectory selects its entire
repository. The command uploads immutable snapshots using the selected profile.
Look Closer uses a score threshold of 80/100 by default. Lower it to return more
tentative areas to investigate; it does not change deep-review filtering:

```sh
doomer review --look-closer-threshold=55
```

By default it runs both reviews: it returns Look Closer results synchronously,
with a link to the deep review continuing on the server. It reuses the same
content-addressed upload cache as `watch` and needs no installed agent hooks.

```sh
# Look Closer only; do not run a deep review:
doomer review --deep-review=false

# Deep review only:
doomer review --look-closer=false
```

All modes use the workspace API and the same user/service-account credentials
as `watch`.

The summary always shows changed files, added/deleted lines, and hunk counts.
Add `--show-diff` to print the exact patch before uploading and running the review:

```sh
doomer review --base origin/main --path ../my-repo --show-diff
```

It defaults to false. The patch goes to stderr so `--format=json` keeps stdout
machine-readable. Non-ignored untracked files are included in the patch.

Enable reviews separately from choosing which results to wait for:

| Look Closer | Deep review | Command |
|---|---|---|
| Sync | Async | `doomer review` (default) |
| Async | Async | `doomer review --wait=none` |
| Async | Sync | `doomer review --wait=deep` |
| Sync | Sync | `doomer review --wait=all` |
| Sync | Disabled | `doomer review --deep-review=false` |
| Async | Disabled | `doomer review --deep-review=false --wait=none` |
| Disabled | Async | `doomer review --look-closer=false` |
| Disabled | Sync | `doomer review --look-closer=false --wait=deep` |

`--wait=look-closer` explicitly selects the default wait behavior. Bare `--wait`
means `--wait=all`, waiting for all enabled stages. Disabling both reviews or
explicitly waiting for a disabled stage is an error. Async commands still wait
for upload and server acceptance; the reviews then run on the server.

To wait for the review and print its results:

```sh
doomer review --wait
doomer review --wait --timeout 20m --poll-interval 5s --format json
```

Waiting starts after the upload is accepted. Look Closer has a 30-second default
wait; Waiting for the deep review raises the default to 30 minutes.
Both poll once per second. `--timeout` and `--poll-interval` override these defaults. Output includes the last known status even on a
wait failure. A failed, cancelled, or expired job, a timeout, or a polling error
exits nonzero. A completed review exits zero regardless of its conclusion or
finding count; workflows can inspect the JSON result for their own policies.
Cancelling the wait does not cancel the hosted job. The review ID remains usable
with `review get --workspace`, so a timeout never requires submitting another review.

Read that submission's status and results with:

```sh
doomer review get <review-id> --workspace
doomer review get <review-id> --workspace --format json
```

`review status <review-id>` is an alias. Omit `--workspace` only when reading
a legacy submission. The response includes the job status,
summary, conclusion, complete findings, and any job error when available. A queued
or running review may not have results yet. Reading a failed job still exits
successfully if the lookup succeeds; inspect `data.status` and `data.error` in
JSON output. Access uses the selected profile and the submission's ownership.
`finding ls` continues to list stored findings across a project; use `review get`
for the findings belonging to a specific CLI worktree review.


The default baseline is the merge base of HEAD and origin's local default-branch
ref, falling back to origin/main, origin/master, main, master, then HEAD. No refs
are fetched. `--base` uses the specified local ref or commit directly. An
unchanged snapshot is rejected; use a baseline containing the changes you want
reviewed. Both clean feature branches and uncommitted work are supported.

Snapshots include tracked files and non-ignored untracked files, with current
working bytes taking precedence over the index. Deleted files appear only in the
baseline. Git metadata and ignored untracked files are excluded. No commits,
index changes, builds, hooks, filters, or local review processes are performed.
The default workspace path permits regular files up to 20 MiB and 1 GiB of
source; each tree can contain at most 20,000 files. All review modes use this upload path. Symlinks, submodules, sparse
checkouts, unresolved merges, and Git LFS pointers cause an explicit error.

SaaS stores repository identity, branch, HEAD SHA, and dirty status alongside verified
baseline and target tree fingerprints. Fingerprints cover every captured path,
file mode, and file's exact bytes, independent of branch names and commits.
Hashing shares the upload capture pass and batches baseline Git object reads.
The same dirty code gets the same target fingerprint after it is committed,
provided the paths, bytes, and executable modes are unchanged. Line-ending or
Git-filter transformations can change that identity. Git metadata is informational;
SaaS recomputes the tree fingerprints from the validated upload.

Repeating the exact capture resumes the same submission, including after a lost
upload/finalize response. The fingerprints remain after snapshot payload cleanup.
Automatic matching or reuse when a PR opens is future work; this change stores
the identities needed for it. See [the fingerprint contract](docs/review-snapshots.md).
The SaaS schema update must be applied through its normal SchemaHero workflow
before using the new CLI review submission metadata.

## Private adversaries

Publish an adversary project containing `adversary.yaml` and its runtime files:

```sh
doomer --profile work login
doomer adversary init my-reviewer
cd my-reviewer
npm ci
npm test
cd ..
doomer adversary pack ./my-reviewer --check
doomer adversary pack ./my-reviewer
doomer --profile work adversary push my-reviewer:1.0.0
# On another machine, use your team's namespace:
doomer --profile work adversary pull my-team/my-reviewer:1.0.0
```

Use the name and version printed by `pack` as the local reference. The default
push destination is `registry.doomer.ai/<team-namespace>/<name>:<version>`.
The CLI uses the selected profile's namespace or retrieves it from your account.
`DOOMER_REGISTRY_NAMESPACE` overrides that namespace. For imported service
account tokens, supply `login --token-stdin --registry-namespace my-team`.

The CLI uploads the package image and layers. SaaS processes hosted uploads:
it extracts `adversary.yaml`, `README.md`, and `CHECKS.md` from the package,
publishes versioned OCI attachments, and signs private team packages. Downloads
verify and cache publisher signatures when available. Hosted processing can
finish after `push` returns; `push` reports the immutable upload digest.
External registries receive the package image and layers without SaaS processing.

```sh
# Override the destination or use Docker credentials for an external registry:
doomer adversary push my-reviewer:1.0.0 ghcr.io/my-team/my-reviewer:1.0.0
doomer adversary pull ghcr.io/my-team/my-reviewer:1.0.0

# Inspect and maintain local packages:
doomer adversary ls
doomer adversary inspect my-reviewer:1.0.0
doomer adversary check
doomer adversary rm my-team/my-reviewer:1.0.0
```

`adversary ls` lists every local reference, including tags pointing to the same
package. `adversary rm` removes one local reference; it leaves remote packages
and shared local content intact. Use `--if-digest sha256:...` when a script needs
to remove a reference only if it still points to an expected digest. Ambiguous
short names require a fully qualified reference from `adversary ls`.

`pack --check` validates the manifest and inventories files without building or
writing artifacts. `pack` runs the project's build script; it supports
`--builder local` (default, npm and Node 22) or `--builder docker`.
`--name` overrides the local artifact name. `pack`, `push`, and `pull` support
`--format json`; the deprecated `--json` alias is retained for compatibility.
Build logs and transfer progress are written to stderr.

Manifests may declare `requires: [go-module-dependencies]`. The CLI validates
and preserves this field when packaging; the hosted review runtime prepares
the dependencies. Packing does not run that prerequisite or download the
reviewed repository's Go modules. Unknown or duplicate prerequisite names are
rejected. Publish such packages only after the hosted runtime supports them.

`DOOMER_DATA_DIR` overrides artifact storage. The store uses the previous CLI's
platform data directory and `repository-v1` format, so existing packages remain
available. References support tags and immutable digests. Loopback registries
use HTTP for local development; remote registries use HTTPS.

## Development

```sh
go test -race ./...
go vet ./...
```

Authentication and credential-storage code has been adapted from the previous
CLI, together with its package builder, OCI registry client, and artifact store.
This repository contains no local review engine or prior release artifacts.

## Releases and Homebrew

Depot CI reads `.depot/workflows/ci.yml` and `.depot/workflows/release.yml`.
Push an immutable CalVer tag such as `2026.10.3` from reviewed `main` to build
and publish a release. Depot also opens the verified Homebrew formula PR and
waits for automatic review/merge into `doomerlabs/homebrew-tap` as `doomer`.
Install with `brew install doomerlabs/tap/doomer`.
Prerelease tags such as `2026.10.3-beta.1` update `doomer-beta` separately.

Release archives include stamped version/commit/build metadata, checksums,
an SPDX dependency graph, and a release manifest. `doomer version` and
`doomer --version` print build metadata. Unstamped builds report version `dev`.
See [release operations](docs/release.md) for the publication and secret setup.

Environment variables use the `DOOMER_` prefix. Legacy `ADVERSARY_*`
variables are ignored, including API and registry overrides. This SaaS bridge
has no local model/provider or Node runtime configuration. `DOOMER_DATA_DIR`
overrides the artifact data directory; credentials remain in the separate
configuration directory.

### Anonymous reviews

No login is required for `review` or `watch`. Direct `review` invocation authorizes sending
source code and review context to the selected service. Anonymous runs always use
Look Closer only, even when Deep review is requested. By default the CLI waits for
Look Closer; `--wait=none` still returns immediately. Existing signed-in profiles
keep their normal review modes.

The unlisted link lets anyone holding it view that one review. It can be saved to
an account after signing in. Unclaimed links expire after seven days. To run Deep
review, log in and rerun the review. Login does not silently merge anonymous
uploads into the account.

Anonymous identities, uploads and hooks are isolated by API URL and profile.
To test locally without changing your existing login:

```sh
./bin/doomer --profile anonymous-local --api-url http://localhost:3000/api review --path /path/to/repo
./bin/doomer --profile anonymous-local --api-url http://localhost:3000/api watch start --browser --agent both
```

Codex still manages hook trust in `/hooks`. Agent feedback keeps using the same
conversation-scoped workflow. Hooks stay quiet when no useful results are ready;
the CLI displays the review link even when Look Closer returns no areas.

### Check your account

```sh
doomer whoami
doomer whoami --profile anonymous-local
doomer whoami --profile work --format json
```

`whoami` shows the profile and service, and verifies a saved login with that
service before showing account, team and plan information. With no login it
reports anonymous mode. It does not create credentials or change profiles.
An expired or rejected login is reported as an error, not as anonymous mode.

### Reuse content across profiles

When the service supports it, the CLI answers a fresh challenge using the entire
local file before reusing content uploaded by another identity. A SHA alone never
grants access. Successful proofs avoid sending the file again; unavailable or
rejected proofs fall back to normal upload. This works for uncommitted changes
and private repositories as well as public code.

`watch` reports the bytes and blobs reused after proving possession. This saves
upload bandwidth; it still reads local files to calculate the proof. Reuse can
reveal that identical content is already stored, but not who uploaded it.

Uploads show preparation and reuse checks, followed by byte progress for each
batch of required content. Progress includes bytes sent, percentage and an
estimated remaining transfer time. Waiting for server confirmation is shown
separately; upload estimates do not include the review itself. Human progress
messages go to stderr for `review`, so `--format json` keeps stdout readable by
other programs.
