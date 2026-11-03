---
name: doomer-release
description: Prepare, publish, verify, or recover a Doomer CLI stable or beta release, including Depot jobs, public downloads, and the Homebrew release PR. Use for CLI release requests in this repository, not ordinary code changes or Mac app releases.
---

# Doomer CLI releases

Use the existing release pipeline. Read [release operations](../../../docs/release.md)
and `.depot/workflows/release.yml` before publishing; they describe the current
jobs, artifact contract, and credential scopes. This skill coordinates those
tools rather than replacing them with a second publisher.

## Version and authorization

- New stable tags always use `YYYY.M.D.N`, with unpadded month/day and a positive
  daily sequence: `.1` for the first release, `.2` for the second, and so on.
  Example: `2026.11.2.1`, then `2026.11.2.2`. Never omit the fourth component.
- New beta tags use `YYYY.M.D.N-beta.B`; `-1` is not a stable-release suffix.
- Honor an explicitly requested version. Otherwise use the user's local date
  and inspect remote tags/releases to choose the next unused daily sequence.
  Do not use the CI runner's UTC date to change the requested release date.
- Legacy three-component tags, stable or beta, remain valid for recovery,
  retries, and comparisons. Never rename,
  move, delete, or overwrite an existing tag or its published artifacts.
- A request to prepare or discuss a release does not authorize pushing its tag.
  A request to publish a release does. Ask only for missing decisions or actions
  beyond that authorization.
- Publishing does not authorize installing, upgrading, uninstalling, or replacing
  the user's local CLI, resetting onboarding, or restarting development servers.

## Before tagging

1. Inspect the working tree and preserve unrelated work. Never commit
   `scripts/stage-skill-dev.py`. Use a clean temporary checkout when an existing
   checkout contains local work; do not stash, reset, or clean it automatically.
2. Fetch `origin/main` and remote tags. Confirm all changes intended for this
   release have merged, including required release tooling changes in the tap.
   If the user asks to check specific PRs first, verify each one explicitly.
3. Verify CI passed for the exact main commit being released. Do not substitute
   successful checks on an earlier commit or assume an open PR will merge.
   Address confirmed failures through reviewed commits. Doomer handles merging;
   never manually merge or bypass protections.
4. Confirm the requested tag is unused remotely. For a recovery request involving
   an existing tag, keep its commit and artifacts and resume publication instead.
5. Use the repository's existing release validation scripts for affected release
   tooling. Do not rebuild and test everything repeatedly when the exact main
   commit already passed the required checks.

Create an annotated tag at the verified **explicit commit SHA**, then push only
that tag. Do not tag whichever commit happens to be checked out. Record the tag
and commit in the release status.

## Follow publication through completion

Monitor the tag's Depot `release.yml` run and its individual jobs. The expected
outputs are:

- A published, non-draft GitHub release in `doomerlabs/doomer`, with the correct
  stable/prerelease flag, four macOS/Linux archives, checksums, SPDX metadata,
  release manifest, and the generated `doomer.rb` (stable) or `doomer-beta.rb`
  (beta) asset. `scripts/publish-homebrew.sh` includes this formula in the
  verified release bundle; the tap receives it separately through its PR.
- Public files under `https://downloads.doomer.ai/cli/releases/VERSION/` whose
  bytes match the GitHub release and SHA-256 checksums.
- `https://downloads.doomer.ai/cli/stable.json` or `beta.json` pointing to that
  version, commit, and verified platform assets. If a newer release legitimately
  won a concurrent promotion, report it rather than regressing the channel.
- The generated `doomerlabs/homebrew-tap` PR merged by Doomer, its formula on
  the tap's default branch, and the publication job successful.
- For stable releases, the public installer skill in `doomerlabs/skills` updated
  to the verified current stable channel, respecting concurrent newer releases
  as described below. Publishing
  the CLI alone does not update a pinned skill.

The bucket is shared with Mac app releases. CLI writes belong only under `cli/`
in `doomer-macos-releases`. Preserve other objects and use the existing scoped
Depot credentials; never print credential values or expand their access to
resolve a release failure.

Independently download the published bundle and verify checksums. Check the
public channel manifest, not just GitHub success. Send
`X-Doomer-Release-Verification: 1` on public verification downloads so these
requests do not inflate customer download analytics. If useful, run the native
archive's version command from a temporary directory without installing it.

## Update the public installer skill

After a stable release's public assets are verified, fetch `cli/stable.json`
again from the public download service. Use its current stable version for
the installer pin, even if a concurrent release advanced it past the release
being monitored. Verify that version's immutable release manifest, commit,
archive URLs, and checksums agree with the channel before proceeding. Do not
regress the channel or an existing newer, verified stable installer pin.

Update
`skills/doomer-review/SKILL.md` and the version description in `README.md` in
`doomerlabs/skills`. Keep the installer pinned to that verified version and its
immutable `releases/VERSION/release-manifest.json`, including matching schema,
archive filenames, URLs, and checksum lookup. Preserve consent, hook trust,
and installation safeguards. Do not replace the pin with a beta or local build.

Validate the skill and fetch the public manifest to verify all four platform
entries. Commit and publish through that repository's authorized workflow;
push directly to main only when the user has authorized it. Fetch the skill
repository's main branch again before publishing; if another release updated
the pin, reconcile against the current channel rather than overwriting it with
an older pin. Confirm the remote
main contains the update before reporting the installer ready. If publication
is waiting on review or authorization, report that pending step explicitly.
Existing users refresh with `npx skills update`; new users install with
`npx skills add doomerlabs/skills --skill doomer-review`.

## Failures and review comments

- Inspect the failed job's logs before retrying. Retry a recoverable failure on
  the same tag using the existing idempotent publisher. Do not move the tag,
  rebuild different bytes under it, manually advance an unverified channel, or
  create a new release merely to hide a failed job.
- Bound unchanged retries: after one retry of the same failure, diagnose or
  report the blocker instead of looping. Code fixes for future releases go
  through a new branch and PR; recovery must preserve already-published bytes.
- A Cloudflare 403/1010 can come from Browser Integrity Check rejecting a client
  such as Python's default user agent. Compare the failing request with a normal
  public download before concluding assets are absent. The scoped production
  configuration is documented in `docs/release.md`; verify it still exists.
  Do not disable security
  controls or change credential scopes without the necessary user authorization.
- Read new Doomer comments on the generated tap PR. Fix confirmed defects on
  that branch with relevant checks, or reply with concrete evidence before
  resolving a disagreement. A generic concern that checksums *might* be wrong
  should be answered with recomputed hashes and the actual published assets,
  not unnecessary formula changes. Never silently dismiss a finding.
- Keep monitoring after comments are resolved: a resolved thread alone does not
  prove the PR merged or that publication completed.

Report meaningful failures and fixes while working. The final result should
name the version and commit, link the release, and state public-channel and
Homebrew status. If any stage remains blocked or pending, say so explicitly;
do not call the release complete just because its tag was pushed.
