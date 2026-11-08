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
