# Doomer CLI

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
doomer review --project my-team

# Select a profile and another worktree:
doomer --profile dev review --project my-team --path ../feature-worktree

# Choose a baseline explicitly:
doomer review --project my-team --base origin/main --format json
```

`--project` accepts a project slug or ID. Omit it to use the default selected
with `doomer project use`, or the only available project. If the saved project is
no longer available, select another project or pass `--project` explicitly.
`--path` defaults to the current directory; a subdirectory selects its entire
repository. The command uploads an immutable snapshot and queues a hosted review
using the selected profile. It returns the review ID and status without waiting
for completion. Hosted reviews must be enabled for the project.

The default baseline is the merge base of HEAD and origin's local default-branch
ref, falling back to origin/main, origin/master, main, master, then HEAD. No refs
are fetched. `--base` uses the specified local ref or commit directly. An
unchanged snapshot is rejected; use a baseline containing the changes you want
reviewed. Both clean feature branches and uncommitted work are supported.

Snapshots include tracked files and non-ignored untracked files, with current
working bytes taking precedence over the index. Deleted files appear only in the
baseline. Git metadata and ignored untracked files are excluded. No commits,
index changes, builds, hooks, filters, or local review processes are performed.
Each file must be regular and at most 2 MiB; each tree can contain at most 20,000
files, and the encoded upload cannot exceed 16 MiB. Symlinks, submodules, sparse
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
