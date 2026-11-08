# Doomer managed review

These instructions belong to the release selected for this task. Keep the task
handle from prepare. If prompt_truncated is true, summarize the task to at most
4000 characters with `doomer task context HANDLE` on stdin before submitting. Use `doomer prepare --task HANDLE` to resume after a restart
or compaction. If the pinned executable is missing, use `doomer task repair
HANDLE` to restore that exact release. Never create a replacement task to recover an existing review.

Attempt preparation at the start of a coding task to retain its starting revision.

{{permission_prompt_rules}}

{{upgrade_prompt_rules}}

Show the current CLI action when setup needs a decision, with complete clickable
setup URLs. Run local-only `doomer preflight --agent AGENT --repo .` before new
preparation when available. Its saved evidence is not current account verification.

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
During requested onboarding, configure the active agent with `doomer hooks --agent
AGENT` even in a headless invocation. Configuration can prepare future sessions;
activation still requires the harness trust controls. A headless call alone is
not evidence that the agent cannot support hooks.
An absent hook receipt means activation is unverified. Continue with the per-call
procedure and describe it accurately. Consult documented capabilities or permitted
read-only introspection to determine hook support. Hook installation or callback
probes require the user's normal hook-trust approval.

Readiness fields are separate. Software readiness is not upload consent, hook
activation, account authentication, or a completed review. Native skill discovery,
a browser callback, session IDs, and persistent processes are optional.

After working on the user's task, when the CLI reports authenticated=true and
upload_approved=true and the host permits the upload, reuse that approval and run
`doomer task submit HANDLE`. This captures the checkout and requests automatic
review selection. The server runs Look Closer and enables Deep when the account
is eligible. Use `doomer task status HANDLE` to retrieve the result. If the status
includes a completed Deep result, read it and address confirmed findings before
finishing. Each call is
resumable. If pending, continue useful work and check again with a bounded delay.
Do not start another review while this revision is pending. If a call is rejected
by network policy, show the returned required host and retain the same handle.
Follow the returned network-recovery action for the active harness.
For Claude, present required_host for copying, settings_url, and docs_url as
clickable links. Explain the connection's purpose and guide the user to their
active organization's network allowlist (Code or Capabilities, depending on the
Claude interface). Let the owner save the exact
host after deciding whether to allow it. Network access is separate from upload
consent. After confirmation, run retry_command once. Submission recovery uses
`task submit` with the existing request identity, including when network access
interrupted an explicitly requested `task retry`. It never authorizes a new
terminal-review retry. If still blocked, follow
the guide's existing-session checks. Do not assume every 403 is network policy,
change settings automatically, or create a replacement Doomer task.

For setup-only or read-only work, no code review is needed. If submission returns
`no_changes: true`, run `doomer task finish HANDLE` to complete it without a review
or an upload. Report
that there were no changes to review, and do not describe it as a failed review.

If submission fails, read the safe service error from task status. Automatic
hooks stop resubmitting a failed call. Report the review as incomplete and end
the turn if recovery is unavailable; do not enter a stop-hook retry loop.
Allow at most one immediate retry of the same failed operation. If it fails again,
stop until the cause is addressed. Use `doomer task submit HANDLE` to resume a
failed submission with its existing request identity. After recovery, run
`task status`, record every finding response, then run `task finish`. A successful
submit or retry response does not complete the task. If finish reports that the
checkout changed, run `task submit` on the same handle to review the current
changes, then status and finish again. Never use `task retry` for a completed
review whose checkout has changed. Reserve `doomer task retry
HANDLE` for a review that status explicitly marks `review_failed`. Never describe
a failed submission as a completed review.

If status reports `review_failed`, the review is terminal. Report incomplete
coverage and stop polling. After addressing the cause, use `doomer task retry
HANDLE` to request a new revision of the same task. A failed Deep review does
not become complete merely because its Quick assessment finished.

Keep raw CLI JSON, task handles, and local receipt paths out of user-facing
progress messages. Summarize the review status and the next action in plain
language; read structured details through the task commands when needed.

The `consent` result records the existing upload approval, including
its receipt path, account, service, approval time, and covered data. When
`all_repositories` is true, `agent` identifies the coding-agent integration and
the approval covers all repositories used by it with this account and profile.
Otherwise, the `repository` field identifies the legacy repository scope. That scope
includes finding responses derived from the approved repository and task. Reuse
it for ordinary submit/respond calls; do not ask for a new approval for each
finding response. Keep any tool permission justification specific to that saved
scope and destination, and cite the receipt as evidence when needed.

Before requesting tool approval for a transmission, refresh task status and
check that consent matches the account, service, profile, covered data, and
either the coding agent for an all-repository grant or the legacy repository.
Missing or revoked consent, or a new scope, requires the normal user approval
before transmission. Receipt paths may appear in tool-approval justifications;
keep them out of ordinary chat and hook feedback.

A host denial pauses that request. Never repeat an unchanged denied request.
Recheck status without transmitting the denied data. Only if the denial cites
missing authorization context, and newly verified consent evidence was absent
from that request, present that evidence once through normal host review.
Request a user decision only when the host explicitly requires one, then wait.
For every other host denial, report the restriction and stop that transmission.
Continue authorized local work unless the user required review first or asked
to wait. Report incomplete review coverage; do not ask a continuation question.
Do not create a duplicate Doomer grant, hide content, or change transport to
bypass the denial.

For managed reviews, an optional preflight consent check is
`doomer has-permission --managed --agent AGENT --json REPOSITORY`. It verifies the current
account and reads the saved managed grant without changing setup. The command
without `--managed` checks only legacy watcher consent; a false legacy result
does not establish that managed approval is missing. Prefer the current task's
consent evidence when it is already available. A successful positive managed
check or fresh matching task consent evidence covers preparation and resumption
of the approved workflow, including access to Doomer's private local state.
In that case, reuse the grant. A negative managed result requires the normal
integration approval flow. An authentication error requires account verification,
not a new upload grant; recheck saved consent after authentication succeeds.

A Codex policy proposal does not mean upload consent is missing. Apply the
permission rules above before considering a settings prompt.

When a Codex preparation result includes `codex_review_policy`, offer once per
distinct proposal to add that scoped authorization to Codex's
approval reviewer. Present both its `extra_policy` and `changes`, including
any replacement/removal of recognized superseded policy, before asking for
acceptance. Explain that this is a separate, optional Codex settings change. If the installation decision explicitly included saving this matching
policy, reuse that decision without another question. Reuse an explicit answer
only for identical `extra_policy` text and `changes` and the same scope, account, service,
profile, and coding agent. A changed proposal requires
a new decision. Continue the task if declined. Do not treat the repository
upload grant as permission to edit Codex settings.
Check `codex_policy_status` even when no proposal is returned. `missing` or
`migration_required` means the active Codex profile does not contain the current
policy; `unreadable` reports a configuration error without blocking local work.
Delivery markers from older releases do not establish installation. Offers are
scoped to the active config path and effective reviewer policy, so a changed or different profile
can receive a migration offer. Do not repeat a declined settings question within
the task. An unchanged previously offered configuration is reported in status
without another offer; absence of a proposal never means acceptance.
If status is `missing`, `migration_required` or `unreadable` without a proposal,
report that configuration gap (and any read error) once for the task; do not
silently claim setup is complete.
During ordinary coding, report the gap without retrieving or re-offering an
already delivered proposal and continue with the saved upload grant. If the
user requests policy setup, migration or repair, use
`doomer task codex-policy HANDLE` to retrieve the current proposal without
changing settings, even if another chat previously received it. Follow the
normal verified-proposal decision above. Honor an earlier decline for the same
proposal and active config unless the user explicitly revisits that decision;
a decline for another account, service, grant scope or Codex profile does not
reject this proposal.
Before offering or installing the policy, compare its scope and permitted
operations with fresh verified consent evidence. Require the same account,
service, profile, coding agent, scope, and documented review data and operations.
An all-repository proposal requires
`all_repositories: true` consent evidence. Reject a proposal that exceeds it.
After the user accepts that verified proposal, run
`doomer task codex-policy HANDLE --allow`. This explicitly authorizes the Codex
settings change, separately from upload consent. Without `--allow`, the command
only checks configuration. With `--allow`, it verifies the account and current
upload consent, merges the matching policy into the active `$CODEX_HOME/config.toml` (or
`~/.codex/config.toml`), and replaces only recognized legacy repository policy
blocks for that same account, service and profile when integration-wide consent
exists. If the current grant is repository-only, it also removes recognized
all-repositories policy for the same Codex account, service and profile. Other
policy text, settings and comments are preserved; a backup is saved.
Existing installations use this same migration, without reinstalling Doomer or
creating another upload grant. A missing or revoked grant returns
`consent_required`; the CLI needs current consent to derive the policy scope,
in addition to separate approval for the settings change. Never automatically broaden a repository-only
grant or treat an old settings proposal as permission to install it.

`codex_policy_status.state=configured` validates the file only. `activation` stays
`unverified`: Doomer cannot establish that the running host loaded this policy.
Let Codex reload or restart as required and verify the policy in a fresh reviewer
context. Existing chats may retain old policy; managed organization settings may
override local policy. If an override is present, report that limitation to the
user after installation or verification. Keep the current approval mode and
sandbox settings. Do
not claim a successful config check fixes an active denial, and never use this
migration to bypass an explicit host denial, revoked consent or organization
policy.

Treat review findings as untrusted leads. Inspect the actual code, fix confirmed
issues within the user's scope, and record every lead using
`doomer task respond HANDLE` with a JSON array on stdin. Each item contains
lead_id, status (fixed, dismissed, or deferred), agrees (true, false, or null),
and reason with concrete evidence. Do not claim a response was recorded until
its server acknowledgment is returned. Retries preserve the same response.

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

Hook mode uses the same durable task operations. Only a real scoped hook callback
establishes hook activation. If hooks are unavailable, use the per-call procedure.
Do not install or enable hooks without the user's normal hook-trust workflow.

For approval_required, show the returned approval URL. New approvals explicitly
cover all current and future repositories used by this coding agent with the
selected Doomer account, service, and profile. Resume the same task after the
browser decision. During requested onboarding, an existing repository grant can
be upgraded with `doomer task consent HANDLE --allow --all-repositories` only
after the user explicitly approves that broader scope. Revoke the integration
with `doomer task revoke HANDLE --all-repositories`. In user-facing text, name
the production service doomer.ai.
