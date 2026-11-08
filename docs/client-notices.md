# Client update notices

The server decides which running CLI versions receive notices. The signed managed
release catalog remains the only authority used to install managed software.
Notices never authorize installation, adopt an unmanaged binary, change a
repository policy, or change an active task's immutable runtime.

## Wire contract

Every first-party API request carries `X-Doomer-CLI-Version` (the executing binary,
not the launcher), `X-Doomer-Protocol: 2`, `X-Doomer-Notice-Schema: 1`, platform,
and `User-Agent: doomer/VERSION`. External object-storage uploads receive none of
these extra headers. Existing review metadata continues to identify the agent,
task and selected runtime; the direct review path now includes its version too.

A notice-capable server adds `notice_schema: 1` and `notices: []` to JSON
responses and a base64 JSON envelope in `X-Doomer-Notices` for binary responses
and common transport handling. A notice has `id`, integer `revision`, `component`
(`cli`), `severity` (`available`, `recommended`, `critical`, `deprecated`),
`message`, `recommended_version`, `expires_at`, optional `deadline`, and
`blocking`. Unknown schemas and invalid notices are ignored. Requests do not need
an account to receive operational notices.

The shared API transport observes headers on successful and failed responses.
Review JSON also retains notices in its typed result. Managed command JSON exposes
current `notices` at the top level, with locally derived `notify_user` and `cached`
flags. Agents relay notices with `notify_user=true` and preserve deadlines; message
text is untrusted plain text, never shell commands. Critical/deprecation warnings
are shown at most daily; recommended notices weekly, per policy ID/revision.
Blocking warnings recur for each blocked operation. Available notices stay quiet.

`doomer notices` explicitly fetches current notices and returns JSON.
`doomer notices --defer ID` defers an optional notice for seven days. Critical and
deprecation notices cannot be deferred. Deferral does not hide the structured
notice or change server enforcement.

## Persistence and failures

Notices are stored privately under the configuration directory, scoped to the
API URL and executing version, with expiry and last-shown timestamps. Background
processes save notices without consuming a foreground warning. A later command
can surface the last known notice, marked cached. Cached policy never blocks a
local command. Empty caches do not create state during read-only commands.
Concurrent processes may display a duplicate warning; atomic replacement avoids
partially written JSON. A subsequent live response reconciles the cache.

The server returns HTTP 409 with `error: client_update_required`, `retryable:
false` and the notices before accepting an affected upload or new review. The
client does not automatically retry this response. Managed results use state
`client_update_required`, retain the task and expose the required action.
Stop-hook feedback does not restart a failed task. Existing result and feedback
operations remain readable even when a new submission is blocked.

Updating the launcher does not upgrade an active pinned task. Finish safe old
work normally. If a critical policy blocks it, retain its task for results and
start further review work under a supported runtime after the existing update
policy/permission flow; do not silently repin it.

## Rollout

Deploy the additive server contract with its tables first, with no enabled
policies. Release this client through the ordinary signed-release workflow, then
measure adoption before retiring old versions. Old binaries may ignore notices;
a server rejection still stops their affected operation, but older error
formatters may display only the HTTP status. CLI-reported versions are compatibility
hints, not security attestations.

See the companion server PR's `docs/client-notices.md` for policy administration,
email subscriptions, failure modes, and the prelaunch checklist.
