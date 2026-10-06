# Authentication trust model

The CLI connects to the Doomer SaaS over HTTPS (loopback HTTP is allowed for
local development). Browser login uses PKCE, a random state, and an exact
loopback callback. Named profiles isolate credentials by API service and name.
Credential writes are atomic and locked; Unix directories/files use 0700/0600.
Logout revokes remotely before removing local credentials; `--local-only`
skips revocation. Tokens are not printed by profile listing or authentication.

`run` uploads a bounded source snapshot to the explicitly selected project.
Both baseline and target trees contain source code, including unchanged tracked
files for review context. Git metadata, history and ignored untracked files are
not sent. Source and the user index are not mutated. Pending artifacts use private
local permissions and contain no authentication tokens.

The server validates hashes, sizes, paths and file types before queue admission.
Source access requires both the submission owner and current project membership.
Worker attempts use uploaded bytes in a private repository; no customer hooks,
filters, build commands or GitHub delivery run for snapshot reviews.
