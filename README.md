# Doomer CLI

A Cobra/Viper client for the Doomer SaaS. This bootstrap supports connection
profiles, login, logout, and version information. `run` captures local source for queued hosted reviews and `reviews show` reads
results. Review execution runs in the SaaS worker.

## Build

Requires Go 1.26.6 or newer.

```sh
go build -o bin/doomer .
./bin/doomer --help
```

## Profiles

```sh
doomer profiles add work --endpoint https://doomer.ai/api
doomer profiles use work
doomer profiles list
doomer --profile work login
doomer --profile work logout
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

Settings live in `settings.yaml` and credentials in `config.json` under the
platform's user configuration directory for `doomer`. `DOOMER_CONFIG_DIR`
overrides that directory for isolated installations or tests. Settings never
contain tokens. The credential format and platform path are compatible with
previous installations; the legacy `~/.adversary/config.json` fallback is
retained when using the default directory.

Profiles separate accounts and API connections, including multiple accounts on
the same service. Login authenticates the selected account without choosing a
project. Project-specific commands will select their project with `--project`;
projects are not saved in profiles.

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
If revocation fails, logout preserves local credentials for retry. Credential
writes are atomic, locked, and restricted to the current user on Unix. Logout
uses compare-and-swap to avoid deleting a concurrently replaced token. API
endpoints require HTTPS, with loopback HTTP allowed for development.

## Development

```sh
go test -race ./...
go vet ./...
```

Authentication and credential-storage code has been adapted from the previous
CLI. This repository starts with a new Git history and contains no local review
engine, package registry implementation, or prior release artifacts.

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

## Hosted reviews from local changes

```sh
doomer run --project my-project                 # capture and wait
doomer run --project my-project --async         # return a queued review ID
doomer run --project my-project --staged        # capture only the index
doomer run --project my-project --base main     # branch commits and local edits
doomer reviews show REVIEW_ID
```

The CLI sends source code to the selected Doomer project. Dirty checkouts use
HEAD as the baseline and capture staged changes, unstaged changes, deletions,
and nonignored untracked files. A clean feature branch compares with its default
branch's merge base; a clean default branch reports no changes. `--base` selects
an explicit merge base; `--staged` captures the index. Tracked ignored files are
included. Git state and source files are never changed by capture.

Reviews use a fixed source snapshot, not the live checkout. Continue editing or
remove the checkout after submission; workers and retries use the same captured
bytes. Closing the terminal does not cancel an accepted review. Output is JSON,
including captured finding excerpts and file hashes; anchors refer to captured
source, not current editor lines.

An interrupted submission prints a key. Run `doomer run --resume KEY` with the
original API/profile to retry without reading the source checkout. Pending
snapshots are private local files under `DOOMER_DATA_DIR` (or the platform data
directory), separate from credentials; they are deleted after remote acceptance.
Server uploads expire after 24 hours. Accepted source is retained for retries and
eligible for removal after seven days and terminal completion; captured result
excerpts remain available.

This first version requires Git and an existing HEAD commit. Source snapshots
are limited to 16 MiB JSON, 2 MiB per file and 20,000 paths per tree. Symlinks,
submodules, sparse checkouts, merge conflicts and LFS pointers are rejected.
Project hosted inference must be enabled. Account logins and project service
accounts are supported; CI repository tokens are not supported for this flow.
A submission requires `registry:push`; read access also accepts `registry:pull`.
The snapshot endpoints must be deployed before using these commands.
