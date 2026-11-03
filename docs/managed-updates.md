# Skill-managed CLI updates

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
it identifies an already-approved checkout and the replacement process's working
directory, not a new watch selection. Passing a different approved checkout does
not retarget the watcher; an unapproved checkout is rejected. Refresh never runs onboarding, opens a
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
