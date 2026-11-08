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

{{permission_prompt_rules}}

Show complete clickable setup URLs, preserving fragments, before question tools.
Use CLI state for approval; a missing binary does not imply missing consent.

Run the CLI where edits happen. In temporary homes, execute returned
`portable_setup_command` arrays before approving setup. Honor reusable versus
this-environment-only access. Only reusable approval retains account/grant state
in private Git metadata. Let the CLI restore it; never copy tokens or rewrite grants.

{{upgrade_prompt_rules}}

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
https://raw.githubusercontent.com/doomerlabs/skills/bdfe83373aae2d0164eed7780e7efc2dccb2b6a7/INSTALL.md and require its SHA-256 to equal
`2ef6ee762e86ad02bacd12365faaabd1286801567bb3b59624dbe590100145ba`
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
