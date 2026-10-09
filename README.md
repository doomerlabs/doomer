# Doomer CLI

A Cobra/Viper client for the Doomer SaaS. It supports connection
profiles, login, logout, packaging, and publishing private adversaries. Code submission and hosted run status are planned;
`run` and local code execution are not implemented.

## Build

Requires Go 1.26.6 or newer.

The Nix development shell supplies Go 1.26.6, Node 22, and the build and CI
tools. With Nix and direnv installed, run `direnv allow` in this checkout to
activate it. You can also enter the shell with `nix develop`.

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

## Private adversaries

Publish an adversary project containing `adversary.yaml` and its runtime files:

```sh
doomer --profile work login
doomer pack ./my-reviewer --check
doomer pack ./my-reviewer
doomer --profile work push my-reviewer:1.0.0
# On another machine, use your team's namespace:
doomer --profile work pull my-team/my-reviewer:1.0.0
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
doomer push my-reviewer:1.0.0 ghcr.io/my-team/my-reviewer:1.0.0
doomer pull ghcr.io/my-team/my-reviewer:1.0.0

# Inspect and maintain local packages:
doomer artifacts list
doomer artifacts inspect my-reviewer:1.0.0
doomer artifacts check
doomer artifacts remove my-team/my-reviewer:1.0.0 sha256:<expected-digest>
```

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
