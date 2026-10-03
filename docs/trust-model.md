# Authentication trust model

The CLI connects to the Doomer SaaS over HTTPS (loopback HTTP is allowed for
local development). Browser login uses PKCE, a random state, and an exact
loopback callback. Named profiles isolate credentials by API service and name.
Credential writes are atomic and locked; Unix directories/files use 0700/0600.
Logout revokes remotely before removing local credentials; `--local-only`
skips revocation. Tokens are not printed by profile listing or authentication.

The current CLI does not run code locally or submit code for hosted runs.
