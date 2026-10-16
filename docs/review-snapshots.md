# Worktree review identity

`doomer review --project SLUG [--path DIRECTORY] [--base REF]` uses the existing
create/upload/finalize snapshot API. The CLI sends the selected profile's token
to all three API calls. SaaS authorizes the project, validates all source bytes,
and queues the existing `uploaded_snapshot` review worker.

The v1 source transport stays compatible with existing workers. Source metadata
is supplied when creating a submission; the server checks that its HEAD SHA
matches the uploaded snapshot's `headRef`. Metadata contains a sanitized origin
identity such as `github.com/owner/repo`, branch name, HEAD SHA, and dirty flag. Credentials,
URL query strings, and local filesystem remote paths are omitted. These are
client-provided hints; they are not proof that a commit contains the uploaded code.

## Canonical tree fingerprint

Algorithm name: `doomer-source-tree-v1`. Compute SHA-256 over:

1. UTF-8 bytes of `doomer-source-tree-v1` followed by one zero byte.
2. For each entry sorted by its path's UTF-8 bytes:
   - Four-byte unsigned big-endian length of the path in bytes.
   - UTF-8 path bytes, using `/` separators relative to the repository root.
   - Six ASCII mode bytes, either `100644` or `100755`.
   - The 32 raw SHA-256 digest bytes of the exact file content.

Directories are implicit. Deletions remove paths from the target tree. Modes,
renames, additions, byte changes, and line-ending changes affect identity.
Commits, authors, timestamps, branch names, the base ref, and JSON encoding do
not. The empty tree has the hash of just the version prefix and zero byte.
`testdata/source-tree-v1.json` in both repositories fixes a shared binary preimage
for ASCII and UTF-8 paths and executable modes.

The upload digest is a separate SHA-256 of the full JSON snapshot bytes. It binds
both trees, refs, modes, and blob data during transport. The submission's
idempotency key additionally binds its repository/branch/HEAD/dirty metadata; it is
scoped by project and authenticated owner on the server.

SaaS writes `tree_hash_version`, `base_tree_hash`, and `target_tree_hash` only
after validating the uploaded bytes, with the payload and uploaded status in
one update. It retains these columns after payload cleanup. `repository`,
`git_branch`, `git_head_sha`, and `worktree_dirty` are separate nullable hint columns. Existing
submissions stay compatible and are not automatically backfilled.

## Future PR matching

Recompute the same fingerprint over the complete PR head tree, then look up
accepted submissions within the authorized project and repository. A matching
target means the captured source bytes and modes match, even when a dirty capture
was later committed, rebased, or squashed. Missing repository identity must not
be treated as a match across arbitrary repositories.

Matching the target alone identifies code; reusing a review also requires a
matching baseline tree and compatible review settings, policies, and coverage.
The schema index includes project, repository, algorithm, target, and baseline.
This change does not automatically attach CLI findings to PRs or skip PR reviews.
