---
name: doomer-review
description: Review edited source with Doomer and reuse approved setup.
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
Never replace an existing baseline. Report the blocker once. For final-submission
coverage, follow Final source review below, including its quiet-continuation exception.

Show complete clickable setup URLs, preserving fragments, before question tools.
Use CLI state for approval; a missing binary does not imply missing consent.

Run the CLI where edits happen. In temporary homes, execute returned
`portable_setup_command` arrays before approving setup or editing. Honor reusable versus
this-environment-only access. Only reusable approval retains account/grant state
in private Git metadata. Let the CLI restore it; never copy tokens or rewrite grants.

## Skill upgrade decisions

Only `update_available` for an older revision (or recognized unversioned legacy
wrapper) is a candidate. `current`, `newer`, and same-version `modified` are not
upgrade offers. Preserve modified files. Report incompatibility/inspection errors
separately. Generic "ask" actions do not override these checks.

For project skills, inspect the PR-base/remote repository's default branch and
open PR heads. If every affected path has the offered revision or later in that
branch or one PR, suppress the offer and duplicate PR. Compare numeric versions,
not titles/partial updates. Keep local files/runtime; do not merge, rebase, or copy.

For personal skills, inspect personal copies; repository updates do not count.

Honor scope/version deferrals and answered/declined offers. New sessions/worktrees
alone create no offer. On ambiguous scope or failed lookup, report and defer.

Offer verified uncovered upgrades once; apply after acceptance, preserving
policy/edits. Explicit upgrade requests count as acceptance. Avoid duplicates.

Resume with `doomer prepare --task HANDLE`; never replace a task to recover it.
For a missing selected runtime, use `doomer task repair HANDLE` through the
available launcher to restore that exact release, then resume the same handle.
Never substitute stable for an active task. Follow structured actions; never
invent consent, account identity, or hook activation. Native discovery is optional.

Follow the selected CLI's instructions subject to the rules above.
Submit only with `authenticated=true`, `upload_approved=true`, and host permission.
Use task commands for results, every finding response, and verified completion.
Pending results or installed software do not mean reviewed. Per-call mode runs
these commands during each task; describe it accurately.
A missing hook receipt means unverified activation, not unsupported hooks.

## Final source review

After fixes or later edits, run `doomer task submit HANDLE` on the same task before
final commit/push or handoff. A fixed finding response does not submit new source.
Without active hooks, submit explicitly. Read results and repeat after edits.
Do not duplicate pending reviews; continue useful work, check with bounded waits,
then submit current source. Verify completion with `doomer task finish HANDLE`.

An explicit user requirement for review before proceeding remains a gate.
Otherwise, if final submission is missed, pending, or unavailable, continue without
warning or asking about missing final local coverage or PR/local version differences.
Never claim unreviewed changes were reviewed. This final-gap exception overrides
skill/runtime/guide reporting rules, never permissions, host denials, or explicit review gates.

For requested installation or onboarding, or when the CLI is missing for the
authorized coding task, fetch the official INSTALL.md at
https://raw.githubusercontent.com/doomerlabs/skills/7f2ab261b4d25526613922e3050fb312f0ba6d62/INSTALL.md and require its SHA-256 to equal
`c2e4c68cd9be32fe358f28d85579448ea93e13a8f6247c78322b27d001fbec95`
before following the relevant verified steps with normal execution permissions.
Fetch raw Markdown through the shell if a reader summarizes it.
Verify the full checksum. Public HTTPS needs no GitHub login.
Keep active task handles/runtimes. Install in the editing environment, preserving
repository bootstraps and host controls, then prepare/resume to reuse saved setup.
For blocked installation, follow the blocked-review rule above. Ask only for
required decisions; never repeat authorized installation or speculate about
missing sign-in/upload grants. Show only refreshed approval requests.

For requested reset, preview `doomer onboard reset --dry-run`, then run
`doomer onboard reset` only with user authorization for that reported cleanup.
It covers discovered installs/profiles,
keeps login, revokes upload/portable/update grants, and rejects old portable copies.
Report incomplete cleanup, restarts, and paused coverage. Keep task handles;
resume only after explicit user re-grant. Never test with live reset or recreate grants.
