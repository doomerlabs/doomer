## Skill upgrade decisions

Read each `bootstrap_updates` entry's complete action and command fields before
summarizing it. If output is saved to a file, read those fields in full; do not
truncate away the commands or treat a preview as the full instructions.

Only `update_available` for an older revision (or recognized unversioned legacy
wrapper) is a candidate. `current`, `newer`, and same-version `modified` are not
upgrade offers. Preserve modified files. Report incompatibility/inspection errors
separately. Generic "ask" actions do not override these checks.

For project skills, inspect the PR-base/remote repository's default branch and
open PR heads. If every affected path has the offered revision or later in that
branch or one PR, suppress the offer and duplicate PR. Compare numeric versions,
not titles/partial updates. Keep local files/runtime; do not merge, rebase, or copy.

For personal skills, inspect personal copies; repository updates do not count.

Honor scope/version deferrals and answered/declined offers. When the user chooses
“Maybe later”, execute the returned `defer_command` before finishing. For older
CLIs, use the exact executable and `bootstrap defer` command from the complete
action; the PATH launcher may be older. Verify `bootstrap_deferred`; a verbal
acknowledgement does not save the choice. Preserve the skill files and policy.
New sessions/worktrees alone create no offer. On ambiguous scope or failed lookup,
report and defer.

Offer verified uncovered upgrades once; apply after acceptance, preserving
policy/edits. Explicit upgrade requests count as acceptance. Avoid duplicates.
