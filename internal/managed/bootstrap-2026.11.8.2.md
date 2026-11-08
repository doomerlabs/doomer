---
name: doomer-review
description: Prepare Doomer review early, reuse installation approval, and report review coverage before completing coding work.
metadata:
{{metadata}}
---

# Doomer review

At task start, run `doomer preflight --agent AGENT --repo .` for local-only
consent and Codex policy inspection. Saved receipts do not verify the current
account or authorize uploads. Then run
`doomer prepare --agent AGENT --repo . --prompt "USER TASK"`.
Use codex or claude and the actual task without shell interpolation. Keep the
returned task handle. An older CLI may lack preflight; continue with prepare.
If a real harness session ID is available, pass it with `--session`. For Claude,
use the active harness's `CLAUDE_CODE_SESSION_ID` when exposed. Chat URL IDs and
invented prefixes do not establish a hook binding.

For setup only, run `doomer prepare --agent AGENT --repo .` without `--prompt`.
A prompt creates a coding task. Verify readiness/hooks without submitting code.

## Permission decisions

New sessions, branches, and linked worktrees are not new installations. Run
prepare and reuse state; do not repeat setup solely because context changed.
Current successful CLI state and consent decide which permission is missing;
helper/guide suggestions to ask do not override these rules:
- Upload: reuse CLI-verified grants for prepare, submit, and responses. Integration
  grants cover repositories for the same account/service/profile/agent. Repository
  grants retain scope, including linked worktrees. Ask only on
  `approval_required`/`consent_required` for missing/revoked/changed consent.
- Software updates: ask only for `update_permission_required` in the applicable
  scope/policy. Skill-upgrade acceptance is separate.
- Hooks/policy: missing receipts, unverified activation, or missing reviewer
  policy do not revoke upload consent. Configure for requested setup/repair or
  confirmed missing components with authorization. Reuse matching settings
  acceptance; perform and verify the accepted installation, not another offer.

Authentication/network errors require recovery and rechecking, not new consent.
Do not retry a stated reviewer/user refusal. Diagnose ambiguous errors read-only.
For a confirmed sandbox restriction, request normal host approval once for
private-state access/account checks. Saved scope is context, not current
consent. Never bypass host controls or broaden grants.
During onboarding, complete any accepted Codex policy installation and inspect
`codex_policy_status`. `configured` means saved, not active. Reload/restart Codex
as needed and verify a permitted Doomer operation in a fresh reviewer session
before claiming activation. Do not broaden settings or probe a denied operation.

If review is blocked, continue authorized local edits and tests by default.
Do not ask whether to continue. Explicit earlier user requirements to wait or
require review before editing take precedence.
Stop the denied transmission; keep uploads blocked. Silence is not upload consent.
Do not retry explicit reviewer refusals. Keep any task handle. If none exists,
record `git rev-parse HEAD` before
editing and later prepare with the original arguments plus `--base STARTING_COMMIT`.
Never replace an existing baseline. Report the blocker once and incomplete review
coverage before finishing. This rule also applies to older runtime instructions.

Show complete clickable setup URLs, preserving fragments, before question tools.
Use CLI state to
identify approval; a missing binary does not imply missing consent.

Run the CLI where edits happen. In a temporary home, execute any returned
`portable_setup_command` array before approving setup. It offers reusable or
this-environment-only access; honor the choice. Only reusable approval retains
account access and the grant in private Git metadata. Let the CLI restore it;
never copy tokens or rewrite grants. Blocked setup does not block local work.

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

Resume an existing task with `doomer prepare --task HANDLE` after interruption.
Never create a replacement task to recover an existing review. If that task's
selected runtime is missing, run `doomer task repair HANDLE` through the available
launcher, then resume the same handle. Repair restores that exact release; do not
substitute the current stable release for an active task. Follow structured
user-action requests. Never invent consent, account identity, or hook activation.
Native skill discovery is optional.

Follow the selected CLI's matching instructions, subject to the prompt rules
above, including default local continuation when review is blocked.
Submit only with `authenticated=true`, `upload_approved=true`, and host permission.
Use task commands to retrieve results, record every finding response, and verify
completion. Pending results or installed software do not establish completed
review. Per-call mode runs these commands during each task; describe it accurately.
A missing hook receipt means unverified activation, not unsupported hooks.

For requested installation or onboarding, or when the CLI is missing for the
authorized coding task, fetch the official INSTALL.md at
https://raw.githubusercontent.com/doomerlabs/skills/7f2ab261b4d25526613922e3050fb312f0ba6d62/INSTALL.md and require its SHA-256 to equal
`c2e4c68cd9be32fe358f28d85579448ea93e13a8f6247c78322b27d001fbec95`
before following the relevant verified steps with normal execution permissions.
Fetch raw Markdown through the shell if a reader summarizes it.
Verify the full checksum. Public HTTPS needs no GitHub login.
Retain active task handles and runtimes. Repair missing task executables with
`doomer task repair HANDLE`; replace runtimes only without an active task.
Install in the editing environment, preserving repository bootstraps. Use normal
host permissions, then prepare to discover saved setup. Blocked installation
uses local continuation above. Ask only for actual required decisions; never
repeat authorized installation or speculate about missing sign-in/upload grants.
After installation, resume any existing task with `prepare --task HANDLE`, or
run the initial prepare command if no task exists. Present only the refreshed
result's approval request; earlier pending requests may have been satisfied.

For requested onboarding reset, preview with `doomer onboard reset --dry-run`,
then run `doomer onboard reset` when authorized. It covers discovered installs
and profiles, keeps login credentials, revokes upload/portable/update grants,
and rejects old portable copies. Report incomplete cleanup and required agent
restarts. Never test with a live reset or automatically recreate access afterward.
Keep paused task handles, report incomplete coverage, and resume only after
explicit renewed access.
