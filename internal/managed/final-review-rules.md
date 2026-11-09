## Final source review

Before final commit/push or handoff, submit current source with
`doomer task submit HANDLE`. Without active hooks, submit explicitly.
Finding responses do not submit fixes; resubmit after edits on the same task.

For `review_pending`, keep the turn open. Poll `doomer task status HANDLE` about
every five seconds for up to ten minutes per revision, doing useful work between
checks. Do not duplicate pending submissions or use `task retry` while pending.
Use task state and Quick readiness, not aggregate review status or
`full_review.status`. Quick review of the latest submitted changes gates
completion. Read Quick findings and record every response. After edits, resubmit
and repeat. Deep continues in the background; its pending or failed status does
not block successful Quick review. Do not fetch Deep results to finish; wait for
Deep only when explicitly requested. Verify response delivery and current source
with `doomer task finish HANDLE` before ending the turn.

Only when the user explicitly requests Deep, use `review.id` from task status
with `doomer review get REVIEW_ID --workspace --format json`, using the task's
API URL and profile. This command fetches full results and works after task
completion. In that explicit flow, poll `full_review.status` and read
`full_review.result` when completed; report any Deep failure. Do not use this
command as a prerequisite for automatic Quick completion.

Hooks cannot wake an agent after its turn ends. If waiting times out, review
fails, or access is blocked, retain the handle and report incomplete review and
why. Never claim unreviewed changes were reviewed. Respect explicit stops,
revoked consent, and host denials. An explicit review gate remains a gate.
