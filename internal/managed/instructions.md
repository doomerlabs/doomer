# Doomer managed review

These instructions belong to the release selected for this task. Keep the task
handle from prepare. If prompt_truncated is true, summarize the task to at most
4000 characters with `doomer task context HANDLE` on stdin before submitting. Use `doomer prepare --task HANDLE` to resume after a restart
or compaction. If the pinned executable is missing, use `doomer task repair
HANDLE` to restore that exact release. Never create a replacement task to recover an existing review.

Before editing, resolve any repository approval or update authorization
request returned by prepare. Show its actual user action and wait for the human.
Never grant permission or manufacture account state on the user's behalf.

Readiness fields are separate. Software readiness is not upload consent, hook
activation, account authentication, or a completed review. Native skill discovery,
a browser callback, session IDs, and persistent processes are optional.

For per-call execution, work normally on the user's task, then run
`doomer task submit HANDLE`. This captures the checkout and submits free Look
Closer only. Use `doomer task status HANDLE` to retrieve the result. Each call is
resumable. If pending, continue useful work and check again with a bounded delay.
Do not start another review while this revision is pending. If a call is rejected
by network policy, show the returned required host and retain the same handle.

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
check that the current consent still matches the account, repository and
service. Receipt paths belong in tool-approval justifications when needed,
not in ordinary chat or hook feedback.

If the host rejects a call, leave that request paused. Never repeat an unchanged
denied request. Recheck status for revocation or a scope/account mismatch. Only
when the rejection identifies missing authorization context and newly verified
consent evidence was absent from the denied request, submit that evidence once
through the host's normal review mechanism. If the host still requires a user
decision, ask once and wait. Do not hide content or change transport to bypass
the denial.

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
