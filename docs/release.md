# Release operations

The Depot release workflow runs on CalVer tag pushes: `YYYY.M.D` or
`YYYY.M.D-beta.N`. Tags must point to commits reachable from `origin/main` and
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
git tag -a 2026.10.3 -m 'Release 2026.10.3'
git push origin 2026.10.3
```

Use a new CalVer tag for corrections. Release retries validate existing assets
and never overwrite published bytes. There is no automatic release on a main
branch push. `go install github.com/doomerlabs/doomer@TAG` works but reports
version `dev`; release archives and Homebrew contain explicitly stamped builds.

## Verify locally

```sh
make ci
RELEASE_MODE=build scripts/publish-homebrew.sh 2099.1.2
RELEASE_MODE=verify scripts/publish-homebrew.sh 2099.1.2
```

Building/verification require no publication credentials. The contract tests
exercise both publication channels using fake GitHub/Git commands, including
retry, tampered-asset rejection, and authority isolation. GNU tar is required
for deterministic archives (`gtar` on macOS).
