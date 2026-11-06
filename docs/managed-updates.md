# Skill-managed CLI updates

## Managed bootstrap wrappers

Protocol-2 wrappers declare `doomer-bootstrap-version` using the first Doomer
release that ships that wrapper revision. `doomer-cli-range` declares supported
four-part CLI versions, for example `>=2026.11.6.9 <2027.1.1.1`. Supported
comparisons are `=`, `>`, `>=`, `<`, and `<=`, joined with spaces. These fields
are separate from the repository's stable, delayed, manual, or pinned policy.
Ordinary CLI releases keep the wrapper revision unchanged.

`prepare` reports `bootstrap_updates` for old, modified, newer, or incompatible
wrappers. The launcher also adds this information when resuming older runtimes.
`doomer bootstrap status --repo .` inspects both repository copies. Use
`--personal` to inspect the wrappers in the user's agent directories.

After the user accepts the offer, `doomer bootstrap upgrade --accept --repo .`
replaces recognized generated wrappers and preserves their update policy.
Use `--personal` for personal installations. Without `--accept`, the command
only reports the offer. Locally modified wrappers require manual reconciliation.
Both destinations are checked before writing, concurrent changes are rejected,
and partial failures restore already-written copies. Existing task handles,
runtime pins, credentials, and repository grants stay intact.

The following sections describe the legacy skill-pinned updater.

The review skill pins a tested stable release. Updating the skill changes that
pin; the CLI does not independently follow the stable channel or a beta.

`doomer self-update --check --version YYYY.M.D.N` reports JSON without network
requests, profile access, or writes. It inspects only `~/.local/bin/doomer` and
`~/.local/share/doomer/installation.json`. A managed installation must have the
recorded path, manager (`doomer-review`), and SHA-256 of its current bytes.
Location alone never establishes ownership.

After the user approves updating, `doomer self-update --version YYYY.M.D.N`
fetches that immutable public release manifest and archive, verifies the archive
checksum, extracts only its regular executable, backs up the existing binary,
and atomically replaces it. A fresh installation writes the same ownership
record. The verified temporary release binary can run this command during
bootstrap, so no installed CLI is required. The installer does not change PATH.

Unknown, manually modified, and symlink installations require explicit user
approval and `--adopt`. Only the private path is replaced; a symlink target such
as a Homebrew binary is never overwritten. A newer managed version is never
silently downgraded. Failed verification leaves the installation untouched;
failed publication restores the previous binary. Updates retain uniquely named
backups next to the binary.

The skill checks its own source updates once per coding session using the skills
package manager, offers one update for the skill and matching managed CLI, and
rereads the updated instructions before selecting the pin. Declining preserves
the existing pair. Updating software does not authorize repository uploads,
change saved consent, or bypass coding-agent hook trust. After replacement, run
`onboard --repo REPO --refresh` separately to replace an existing managed watcher
using its original directory selection. `--repo` defaults to the current directory;
it identifies an already-approved checkout for the permission check, not a new
watch selection or working directory. Passing a different approved checkout does
not retarget the watcher; an unapproved checkout is rejected. The replacement
uses the watcher's recorded working directory, validated before stopping it.
If that directory is missing, invalid, or absent from an older process record,
refresh leaves the old watcher running and asks for an explicit stop/restart;
it never guesses from the profile's approved repositories. Refresh never runs onboarding, opens a
consent page, changes hooks, or replaces saved task/activation state. It cannot
be combined with `--restart`, `--task-stdin`, or `--check-hooks`. An absent watcher
returns `not_running`; paused or foreground watchers are left alone and reported
as errors. Normal onboarding remains a separate operation for new setups.

Replacing the executable at the same absolute path does not change the installed
hook commands, so an already-loaded, trusted integration needs no new activation
solely for this refresh. Refresh does not prove hook trust or live delivery. If
hooks are missing, changed, untrusted, or have not loaded in the current agent
session, complete normal onboarding and that agent's activation/trust flow
separately. Never treat watcher replacement as hook activation.

Older release binaries do not have `self-update`. The skill retains its verified
bootstrap path for them until the pinned release includes the updater. Do not
claim those older manual installations have a managed ownership record.

## Side-by-side upgrades and watcher lifetime

`self-update --side-by-side --version VERSION` preserves immutable releases and
advances the managed global launcher to the newest verified installed release.
Installing an older checkout pin never downgrades the launcher. The JSON result
includes `launcher_version`. A retry completes launcher promotion and supervision
even when the selected release was installed before an interrupted upgrade.
An unmanaged global command still requires explicit adoption. Initial migration
from a legacy launcher retains the existing repository compatibility checks.

The launcher owns one lightweight supervisor per profile. It supervises older
protocol-1 workers through their existing graceful pause mechanism. Workers
without recent editing activity or pending review work are suspended after a
two-minute grace period. Explicit checkout registration does not extend this
process lifetime. Pending captures and reviews retain their worker for up to
30 minutes, so abandoned state cannot keep it alive forever. Foreground workers
and explicit user pauses are preserved. Failure to stop is recorded in the
profile's `supervisor/watch.log`.

An automatically suspended runtime reports `repository_status: "Idle"` and
`paused: false` in current CLI status output. The next mutating hook renews its
lease and resumes it. Read-only hooks do not resume idle workers. The dispatcher
retains the current task prompt locally and replays it once before the first
edit if the selected worker was asleep. Cross-checkout completion events still
reach the runtimes that own their turn. Explicit user pauses disable dispatch.

`runtime-info` advertises `managed_upgrade: 1` and `watch_supervisor: 1` for this
workflow. Older releases require the separate launcher installation step and do
not themselves guarantee bounded worker lifetime. Installing a release and
starting supervision are distinct from repository consent and hook activation.
