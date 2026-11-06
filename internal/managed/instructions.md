# Doomer managed review

These instructions belong to the release selected for this task. Keep the task
handle from prepare. Use `doomer prepare --task HANDLE` to resume after a restart
or compaction. Never create a replacement task to recover an existing review.

Before editing, resolve any authentication, permission, or update authorization
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
