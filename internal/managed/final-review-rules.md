## Final source review

Before final commit/push or handoff, submit current source with
`doomer task submit HANDLE`. Without active hooks, submit explicitly.
Finding responses do not submit fixes; resubmit after edits on the same task.

For `review_pending`, keep the turn open. Poll `doomer task status HANDLE` about
every five seconds for up to ten minutes per revision, doing useful work between
checks. Do not duplicate pending submissions or use `task retry` while pending.
A completed Quick assessment does not complete Deep: wait for Deep when enabled,
read results, address findings, and record responses. After edits, resubmit and
repeat. Verify completion with `doomer task finish HANDLE` before ending the turn.

Hooks cannot wake an agent after its turn ends. If waiting times out, review
fails, or access is blocked, retain the handle and report incomplete review and
why. Never claim unreviewed changes were reviewed. Respect explicit stops,
revoked consent, and host denials. An explicit review gate remains a gate.
