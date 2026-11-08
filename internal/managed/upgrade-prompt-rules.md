## Skill upgrade decisions

Only `update_available` for an older revision (or recognized unversioned legacy
wrapper) is a candidate. `current`, `newer`, and same-version `modified` are not
upgrade offers. Preserve modified files. Report incompatibility/inspection errors
separately. Generic "ask" actions do not override these checks.

For project skills, identify the target repository from the PR base or established
remote. Read its actual default branch and open PR heads. If every affected path
has the offered revision or later in that branch or one PR, suppress the question
and duplicate PR. Compare numeric version parts; titles/partial updates do not
count. Keep this checkout's files/runtime; do not merge, rebase, or copy files.

For personal skills, inspect the actual personal copies; repository updates do
not satisfy them.

For both scopes, honor saved scope/version deferrals and answered or declined
offers in this session. A new session/worktree alone creates no new offer.
On ambiguous scope or failed lookup, report it and defer without asking.

Only offer a verified uncovered upgrade once, then apply after acceptance while
preserving policy and local edits. Explicit upgrade requests supply acceptance;
still avoid duplicate work.
