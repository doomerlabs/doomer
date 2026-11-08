---
name: doomer-review
description: Prepare Doomer review early, reuse installation approval, and report review coverage before completing coding work.
metadata:
{{metadata}}
---

# Doomer review

At the start of a coding task, attempt `doomer prepare --agent AGENT --repo . --prompt "USER TASK"`.
Replace AGENT with codex or claude and supply the actual task without shell
interpolation. Read the returned instructions and retain the task handle.
If a real harness session ID is available, pass it with `--session`. For Claude,
use the active harness's `CLAUDE_CODE_SESSION_ID` when exposed. Chat URL IDs and
invented prefixes do not establish a hook binding.

Reuse the saved integration approval across repositories, worktrees, and chats
for the same coding agent, Doomer account, service, and profile. Existing
repository-specific grants retain their original scope until explicitly upgraded.
Present a returned `approval_required` action for missing or revoked approval.
If Doomer is blocked, explain why and ask once whether to continue local edits
without a completed Doomer review or wait. Honor that choice for the task.
Continuing edits does not authorize a blocked upload. Keep the task handle and
resume it when possible. If preparation never created a task, record
`git rev-parse HEAD` before editing. Later create the task with the original
prepare arguments plus `--base STARTING_COMMIT`. Keep an existing task's baseline. Report incomplete review coverage before finishing.

Show setup URLs as clickable Markdown links with a short label, preserving the
complete URL including its fragment. Present the link in regular assistant chat
before using a question or choice tool. Use the CLI's returned state to determine
which approval is needed. An absent executable alone says nothing about saved
authentication or grants.

Run the CLI in the environment that edits the checkout. In Cowork or another
environment with a temporary home, execute the returned `portable_setup_command`
argument array when present, before approving setup or editing. It invokes the
selected runtime directly and offers reusable setup or access in this environment
only. Honor the user's choice. Only reusable approval retains account access and
the repository grant in private Git metadata for later sessions. The CLI restores
approved setup itself. Never copy tokens or rewrite grant files. The initial
prepare command above remains usable with older launchers.

If prepare reports a bootstrap upgrade, offer it to the user and apply it only
after acceptance. Keep the repository's update policy and any local edits.

Resume an existing task with `doomer prepare --task HANDLE` after interruption.
Never create a replacement task to recover an existing review. If that task's
selected runtime is missing, run `doomer task repair HANDLE` through the available
launcher, then resume the same handle. Repair restores that exact release; do not
substitute the current stable release for an active task. Follow structured
user-action requests. Never invent consent, account identity, or hook activation.
Reading this file directly is sufficient; native skill discovery is optional.

The selected CLI supplies its matching full instructions. Continue local edits
when the user chose to do so despite blocked review, including when an older
runtime still describes preparation as an edit gate. Submit only when the CLI
reports `authenticated=true` and `upload_approved=true`, reusing the installation
grant without another approval prompt. Honor any host denial. Use task commands
to submit changes, retrieve results, record every finding response, and verify
completion. A pending result or installed CLI does not establish a completed review.
In per-call mode, the agent runs these commands as part of each coding task.
Describe that workflow accurately. A missing hook receipt means activation is
unverified; it does not establish that the harness lacks hook support.

For requested installation or onboarding, or when the CLI is missing for the
authorized coding task, fetch the official INSTALL.md at
https://raw.githubusercontent.com/doomerlabs/skills/22f02d667370ffc06ceddf5ba5e24e1896684e12/INSTALL.md and require its SHA-256 to equal
`646b3cea1fcde88bfba31073b0b45e6b7f01092e5a7ac394b0627373f969f951`
before following the relevant verified steps with normal execution permissions.
For an active review task, retain its handle and selected runtime. Use the guide's
onboarding steps with that task. If its executable is missing, use
`doomer task repair HANDLE`; runtime replacement is for setup without an active task.
If installation is blocked, use the local-edit choice above. In Cowork, install
into the editing VM's personal environment, preserving the repository bootstrap.
Perform the verified prerequisite installation under the host's normal execution
permissions, then run prepare to discover and reuse saved setup. Ask the user
only for an actual permission or decision required by that flow. Do not bundle
installation with speculative requests to sign in again or approve uploads.
A separate setup-choice question is unnecessary when this verified installation
is already permitted. Let the host's tool permission controls request approval
when required.
After installation, resume any existing task with `prepare --task HANDLE`, or
run the initial prepare command if no task exists. Present only the refreshed
result's approval request; earlier pending requests may have been satisfied.

When the user requests an onboarding reset, run `doomer onboard reset --dry-run`
to report discovered agent installations, Doomer profiles, and grant revocations, then run
`doomer onboard reset` when that cleanup is authorized. Reset covers discovered
installations and Doomer profiles, keeps login credentials, and revokes review
and update grants. Report any incomplete/manual cleanup and the need to restart
running agents. Do not automatically onboard again or recreate grants after a
reset. Do not run a live reset merely to test an implementation.
Retain existing task handles and report incomplete review coverage for tasks
paused by the reset. Resume them only after the user explicitly grants access again.
