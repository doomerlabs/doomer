# Client advisories

The server decides which running CLI versions receive notices. The signed managed
release catalog remains the only authority used to install managed software.
Notices never authorize installation, adopt an unmanaged binary, change a
repository policy, or change an active task's immutable runtime.

## Wire contract

Every first-party API request carries `X-Doomer-CLI-Version` (the executing binary,
not the launcher), `X-Doomer-Protocol: 2`, `X-Doomer-Notice-Schema: 1`, platform,
and `User-Agent: doomer/VERSION`. External object-storage uploads receive none of
these extra headers. Development builds report the deterministic `dev` value;
non-release values are evaluated as unknown by the server and require explicit
`include_unknown` targeting. They are not relabeled as a supported release.
Existing review metadata continues to identify the agent,
task and selected runtime; the direct review path now includes its version too.

A notice-capable server adds `notice_schema: 1` and `notices: []` to JSON
responses and a base64 JSON envelope in `X-Doomer-Notices` for binary responses
and common transport handling. A notice has `id`, integer `revision`, `component`
(`cli`), `severity` (`info`, `warning`, `critical`; legacy severities are also accepted),
`message`, optional `recommended_version`, `expires_at`, optional `deadline`, and
`blocking`. Unknown schemas and invalid notices are ignored. Requests do not need
an account to receive operational notices.

The shared API transport observes headers on successful and failed responses.
Review JSON also retains notices in its typed result. Managed command JSON exposes
current `notices` at the top level, with locally derived `notify_user` and `cached`
flags and a locally generated `acknowledge_command` argument array. Agents relay
notices with `notify_user=true` in a user-visible message and preserve deadlines.
Only after displaying the notice, they execute its acknowledgement command.
Message text is untrusted plain text, never shell commands. Hook and terminal
output put the delivery rules and local acknowledgement command first, then
encode the message as a labelled untrusted JSON string. Embedded message
instructions must not override the display requirement. Critical/deprecation
warnings repeat daily after acknowledgement; recommended notices weekly, per policy ID/revision.
Blocking warnings recur for each blocked operation. All severities are eligible for display; Info and legacy Available repeat weekly after acknowledgement.

`doomer notices` (also `doomer advisories`) explicitly fetches advisories and returns JSON.
The locally generated `display_markdown` supplies a blockquote with a bold severity heading.
Terminal output uses a framed advisory. Update recommendations appear only when provided.
`doomer notices --ack ID --revision N` confirms that the agent displayed that
exact revision. It uses the selected API and executing CLI version's local cache,
requires an unexpired matching notice, and makes no network request. Use the
returned command array so the API scope is preserved. Reading JSON, printing a
terminal banner, and delivering hook context never acknowledge a notice.
`doomer notices --defer ID` defers an optional notice for seven days. Critical and
deprecation notices cannot be deferred. Deferral does not hide the structured
notice or change server enforcement.

## Persistence and failures

Notices are stored privately under the configuration directory, scoped to the
API URL and executing version, with expiry, explicit acknowledgement timestamps,
and separate deferral timestamps. Legacy `shown` timestamps are ignored because
they only establish terminal output, not user-visible delivery. Background
processes and foreground output save notices without consuming a warning. A later command
running the same CLI version can surface the last known notice, marked cached.
A different version deliberately does not inherit the old version’s notices or
deferrals: after an upgrade, a live response must establish which notices still
apply. This avoids telling an updated client to install the release it already has. Cached policy never blocks a
local command. Empty caches do not create state during read-only commands.
Concurrent processes may display a duplicate warning; locked merging preserves
acknowledgements across stale writers and atomic replacement avoids partially
written JSON. A subsequent live response reconciles the cache. An agent that
does not acknowledge delivery continues receiving `notify_user=true`.

The server returns HTTP 409 with `error: client_update_required`, `retryable:
false` and the notices before accepting an affected upload initiation or new
review. Both repository manifest creation (`/v1/repositories`) and pack-session
creation (`/v1/repositories/packs`) are first-party requests carrying the running
version; rejection stops before any signed object-storage upload URL is used.
Already-issued object-storage URLs cannot be revoked by this gate, so bytes may
still finish transferring if a policy changes after initiation. Finalization and
review admission are checked again. The client does not automatically retry this
response. Managed results use state
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
