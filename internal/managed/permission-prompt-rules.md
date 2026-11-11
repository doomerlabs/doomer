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
A filesystem permission error is not an explicit user/reviewer denial.
For a confirmed sandbox restriction or `state_access_required`, request normal
host approval once for the same command with private-state access/account checks.
Use `retry_command`, `retry_env`, and `retry_cwd` when supplied; preserve stdin when
required. Saved scope is context, not current consent. Never bypass host controls
or broaden grants. If that approval is explicitly denied, stop only that Doomer
operation; continue permitted coding and tests. Report the operation and path,
who denied it and their actual reason if available, and the exact terminal
recovery command. Do not invent a reason or re-request a refused approval.
Preserve any task handle and baseline so the user can run the command in their
terminal and ask you to resume. A recovery command requiring stdin is incomplete
without its original payload; say so without exposing it in shell arguments.
If the host denies execution before the CLI can return recovery details, record
`git rev-parse HEAD` before editing. Include that exact commit as `--base` inside
the complete terminal recovery command, with the original agent, repository,
prompt, service, and profile; do not leave a separate instruction to add it.
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
coverage, follow Final source review below.
