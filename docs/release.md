# Release operations

Coding agents should use the repository's
[`doomer-release` skill](../.agents/skills/doomer-release/SKILL.md) for release
requests. It records version selection, publication gates, recovery, and the
checks required before reporting a release complete.

The Depot release workflow runs on CalVer tag pushes: `YYYY.M.D.N` or
`YYYY.M.D.N-beta.B`. New stable releases always include a daily sequence, starting
at `.1` and incrementing for each stable release that day. Successive betas for
the same stable version keep the base and increment the beta suffix, such as
`2026.11.18.1-beta.1` followed by `2026.11.18.1-beta.2`. Legacy three-component
tags remain valid for retries and comparisons. Tags must point to commits reachable from `origin/main` and
must never move after publication. A release tag starts a read-only build/test
job that cross-builds macOS and Linux amd64/arm64, validates SPDX 2.3 metadata,
and produces deterministic archives, checksums, and a release manifest.

Two separate jobs in the `release` environment recheck the immutable tag and
exact bundle. The GitHub publication job creates/verifies assets and promotes
the completed draft release. Only after that succeeds may Homebrew publication
push the verified formula to `release/doomer-TAG` in `doomerlabs/homebrew-tap`.
The tap's Depot workflow verifies the formula against the public release asset,
opens or reuses its pull request, and merges once required review/checks pass.
The existing Doomer automated reviewer supplies the approval. The release job
waits until the exact formula is on the tap's default branch before succeeding.
There is no direct push to the protected default branch or administrator merge.

Stable releases publish `Formula/doomer.rb` and install the `doomer` binary.
Prereleases publish `Formula/doomer-beta.rb` and install `doomer-beta`.
The archive binary remains named `doomer` in both channels.

## Setup

The new `doomerlabs/doomer` GitHub repository must be enabled in Depot CI's
GitHub installation. Depot must provide a repository write token through
`github.token` for the isolated `publish-github` job. The existing Depot secret
`HOMEBREW_TAP_TOKEN` must match this repository and allow Contents read/write
on `doomerlabs/homebrew-tap`; it is exposed only in the final Homebrew step.
It needs no Pull requests permission. Enable Depot CI for the tap too; its
`publish-formula.yml` workflow uses a tap-scoped `github.token` with Contents and
Pull requests write permissions to open and merge the release PR. If automated
review fails or publication takes more than ten minutes, Depot fails visibly;
retrying reuses the same immutable release branch instead of force-pushing.
Publisher jobs reference the `release` environment. Configure any desired
reviewers and tag restrictions in the connected platform.

## Publish

After Depot CI passes for `main`:

```sh
git tag -a 2026.11.2.1 -m 'Release 2026.11.2.1'
git push origin 2026.11.2.1
```

Use a new CalVer tag for corrections. Release retries validate existing assets
and never overwrite published bytes. There is no automatic release on a main
branch push. `go install github.com/doomerlabs/doomer@TAG` works but reports
version `dev`; release archives and Homebrew contain explicitly stamped builds.

## Verify locally

```sh
make ci
RELEASE_MODE=build scripts/publish-homebrew.sh 2099.1.2.1
RELEASE_MODE=verify scripts/publish-homebrew.sh 2099.1.2.1
```

Building/verification require no publication credentials. The contract tests
exercise both publication channels using fake GitHub/Git commands, including
retry, tampered-asset rejection, and authority isolation. GNU tar is required
for deterministic archives (`gtar` on macOS).


## Public CLI downloads (R2)

After GitHub publication, `publish-r2` verifies the same release bundle before
receiving its R2 credentials. Configure Depot secrets `R2_ACCOUNT_ID`,
`R2_BUCKET`, `R2_ACCESS_KEY_ID`, and `R2_SECRET_ACCESS_KEY` for this repository,
the `release` environment, and `release.yml`. Set `R2_BUCKET=doomer-macos-releases`; the publisher rejects any other bucket
before uploading. The object read/write credential
must be scoped only to `doomer-macos-releases`; it needs no account administration
or other bucket access.

The existing custom domain `downloads.doomer.ai` serves that bucket. CLI
publication only writes under `cli/`, preserving all Mac app release objects.
Versioned CLI assets live under `cli/releases/VERSION/` with immutable cache headers. Existing keys are created
conditionally and can only be reused if their bytes match. The job verifies all
public downloads before promoting `cli/stable.json` (stable tags) or `cli/beta.json`
(beta tags). Channel pointers use `no-store` cache headers and conditional writes
so concurrent releases and retries cannot replace a newer version with an older
one. The pointers contain a schema version, release version, commit, and each
supported platform's archive URL and SHA-256. No GitHub API access is needed by
an installing skill.

A failed R2 publication does not remove the GitHub release or update the channel
pointer. Retry the failed job with the same immutable tag. To backfill an existing
release, download its exact published bundle, verify it with the release bundle
verifier, then run `scripts/publish-r2.py --dir PATH --version VERSION` using the
same bucket-scoped environment variables, including
`R2_BUCKET=doomer-macos-releases`. Never rebuild or overwrite that tag's
published assets.

### Managed macOS installer

Each stable or beta tag also produces a Developer ID Installer–signed
`Doomer-MDM-VERSION.pkg` for Apple Silicon and Intel Macs. The native
[GitHub signing workflow](../.github/workflows/mdm-release.yml) verifies the
published GitHub CLI manifest and its two macOS archives, then combines those
exact binaries. The package contains both agent integrations. The managed
profile still selects which agents can enroll and run hooks.

Before tagging, provision the GitHub `mdm-release` environment in
`doomerlabs/doomer` with `MDM_INSTALLER_P12_BASE64` and
`MDM_INSTALLER_P12_PASSWORD` from the Apple signing credentials. Restrict
deployments to release tags. The macOS job uses a temporary keychain.
Depot reads the signed build artifact with its read-only GitHub Actions
token and uses the existing R2 secrets for publication.

The package and its provenance file are available at
`https://downloads.doomer.ai/cli/releases/VERSION/Doomer-MDM-VERSION.pkg` and
`https://downloads.doomer.ai/cli/releases/VERSION/mdm-release.json`. Depot's
`publish-mdm-r2` job verifies the signing run's tag and commit, checks the
package against its provenance and source CLI manifest, then verifies both
public downloads. The separate `publish-r2` job advances the CLI channel as
soon as the CLI downloads pass verification.

Depot waits up to 100 minutes for the signer. This covers its 85-minute job
timeout plus 15 minutes for runner scheduling. The Depot package job has
a 120-minute limit to leave time for setup and publication.

If the native signing job fails, rerun it and retry only `publish-mdm-r2` on
the same tag. The signing workflow reuses its retained artifact on a rerun.
To recover after it expires, use the exact retained or already published
package and provenance with
`publish-r2.py --dir CLI_BUNDLE --version VERSION --mdm-dir MDM_BUNDLE`.
Timestamped signatures can change on rebuild, so already published package
bytes are immutable.

### Cloudflare download-client compatibility

The active configuration rule **Allow public CLI download clients** disables
Browser Integrity Check only when this expression matches:

```text
(http.host eq "downloads.doomer.ai" and starts_with(http.request.uri.path, "/cli/") and http.request.method in {"GET" "HEAD"})
```

Rule ID: `99093c11887e471f8caba13f84d8b3a3` in the `doomer.ai` zone, Replicated
account. Other paths, methods, and protections are unchanged. The rule is
required because Browser Integrity Check rejected the release publisher's
Python client with HTTP 403 / error 1010 even when normal curl downloads worked.
Do not broaden it to the whole site. Verify the saved configuration if that
failure returns; changing security settings requires appropriate authorization.
The existing approved rule does not require a new permission prompt on every
release. Release verification requests should carry
`X-Doomer-Release-Verification: 1` to exclude them from download analytics.

### Public installer skill

Stable release completion includes publishing the signed `cli/managed.json`
catalog with `DOOMER_RELEASE_SIGNING_KEY` from the protected release environment.
Its public key is embedded in the CLI. Ordinary releases do not change the
bootstrap skill or repository policies. See [managed runtime](managed-runtime.md).

### Automatic update discovery

Channel pointers include an additive `releases` list of published version/commit
pairs. Conditional promotion retains concurrent releases and the previous channel
head. Older clients ignore this field. History begins with that previous head;
this does not backfill every historical tag. The web service uses this feed for
its CLI releases admin and automatic update notices. The beta Node package must
also pin the release before the service advertises it as an installable update.
