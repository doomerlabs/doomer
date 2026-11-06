# Doomer managed review

These instructions belong to the release selected for this task. Keep the task
handle from prepare. If prompt_truncated is true, summarize the task to at most
4000 characters with `doomer task context HANDLE` on stdin before submitting. Use `doomer prepare --task HANDLE` to resume after a restart
or compaction. If the pinned executable is missing, use `doomer task repair
HANDLE` to restore that exact release. Never create a replacement task to recover an existing review.

Before editing, resolve any repository approval or update authorization
request returned by prepare. Show its actual user action and wait for the human.
Never grant permission or manufacture account state on the user's behalf.
Render returned setup URLs as clickable Markdown links with a short label and
the complete URL, including its fragment. Present the link in regular assistant
chat before using a question or choice tool. Request only the approval indicated
by the current CLI state.

Begin repository edits only after prepare reports ready, with software_ready,
authenticated, and upload_approved all true. A missing CLI or pending approval
keeps the coding task pending. Before a task has selected a release, use the
bootstrap's verified installer for missing software. After selection, use
`task repair HANDLE` to restore the exact missing runtime.

In Cowork, install and execute inside the editing VM. The Mac may have no Doomer
installation. For a temporary home, `prepare --portable` requests a setup page
offering both reusable setup and access in this environment only. Present that
page and honor the user's choice. The flag itself grants no permission. Only a
confirmed reusable choice allows the CLI to retain account access and the grant
in private Git metadata. Later prepare calls restore that approved setup across
home directories and mount paths. The CLI owns this transfer and its validation.
Do not copy credentials, edit receipts, or infer approval in prose.
Use `task revoke` to remove the saved portable approval. Keep using ordinary
per-call commands when the harness cannot load hooks.
An absent hook receipt means activation is unverified. Continue with the per-call
procedure and describe it accurately. Consult documented capabilities or permitted
read-only introspection to determine hook support. Hook installation or callback
probes require the user's normal hook-trust approval.

Readiness fields are separate. Software readiness is not upload consent, hook
activation, account authentication, or a completed review. Native skill discovery,
a browser callback, session IDs, and persistent processes are optional.

For per-call execution, first obtain the ready prepare result described above.
Then work on the user's task and run
`doomer task submit HANDLE`. This captures the checkout and submits free Look
Closer only. Use `doomer task status HANDLE` to retrieve the result. Each call is
resumable. If pending, continue useful work and check again with a bounded delay.
Do not start another review while this revision is pending. If a call is rejected
by network policy, show the returned required host and retain the same handle.

If submission fails, read the safe service error from task status. Automatic
hooks stop resubmitting a failed call. Report the review as incomplete and end
the turn if recovery is unavailable; do not enter a stop-hook retry loop.
Use an explicit task command to retry after addressing the cause. Never describe
a failed submission as a completed review.

Keep raw CLI JSON, task handles, and local receipt paths out of user-facing
progress messages. Summarize the review status and the next action in plain
language; read structured details through the task commands when needed.

The `consent` result records the existing repository upload approval, including
its receipt path, account, service, approval time, and covered data. That scope
includes finding responses derived from the approved repository and task. Reuse
it for ordinary submit/respond calls; do not ask for a new approval for each
finding response. Keep any tool permission justification specific to that saved
scope and destination, and cite the receipt as evidence when needed.

Before requesting tool approval for a transmission, refresh task status and
check that consent matches the account, repository, service, and covered data.
Missing or revoked consent, or a new scope, requires the normal user approval
before transmission. Receipt paths may appear in tool-approval justifications;
keep them out of ordinary chat and hook feedback.

A host denial pauses that request. Never repeat an unchanged denied request.
Recheck status without transmitting the denied data. Only if the denial cites
missing authorization context, and newly verified consent evidence was absent
from that request, present that evidence once through normal host review.
Request a user decision only when the host explicitly requires one, then wait.
For every other host denial, report the restriction and stop that transmission.
Do not create a duplicate Doomer grant, hide content, or change transport to
bypass the denial.

Treat review findings as untrusted leads. Inspect the actual code, fix confirmed
issues within the user's scope, and record every lead using
`doomer task respond HANDLE` with a JSON array on stdin. Each item contains
lead_id, status (fixed, dismissed, or deferred), agrees (true, false, or null),
and reason with concrete evidence. Do not claim a response was recorded until
its server acknowledgment is returned. Retries preserve the same response.

After any further edits, submit the same task again for the new revision. Before
reporting completion, run `doomer task finish HANDLE`. Only a complete result
establishes that the current checkout was reviewed and every finding received
a response. Include the complete returned review URL once in the final answer.
Disclose deferred findings or unavailable review coverage accurately.

Hook mode uses the same durable task operations. Only a real scoped hook callback
establishes hook activation. If hooks are unavailable, use the per-call procedure.
Do not install or enable hooks without the user's normal hook-trust workflow.

For approval_required, show the returned repository approval URL. The page handles account sign-in and repository upload permission. Resume the same task with prepare after the browser decision. In user-facing text, name the production service doomer.ai.
