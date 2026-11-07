# Codex installation experiment

One opt-in end-to-end scenario runs real Codex CLI 0.167.0 with GPT-6.1 Terra
at low reasoning effort in a fresh Linux exe.dev VM. Codex reads the public
Doomer install guide and installs the published, signature-verified runtime.
The local fixture provides the account/approval endpoints only. Neither a
production Doomer account nor an existing Codex installation is used.

## Run

From the repository root, with `go`, `ssh`, `scp`, `ssh-keyscan`, and
`ssh-keygen` available, put these secrets in Doppler `doomer/test`:

- `TEST_OPENAI_API_KEY`
- `TEST_EXE_SSH_PRIVATE_KEY` with its public key registered in exe.dev

```sh
doppler run --project doomer --config test -- \
  go run ./tests/codexinstall --budget 0.20 --output /tmp/codex-install-report.json
```

Run fixture and budget tests without credentials or model calls:

```sh
go test ./tests/codexinstall
```

## What passes

- Codex creates both repository skill files and the Doomer launcher.
- The agent asks for update permission and presents the repository approval URL.
- The test responds affirmatively and approves the pending fake account grant.
- The same saved task reaches software-ready, authenticated, upload-approved state.
- Codex configures its Doomer hooks.
- Exactly one repository approval is created and no repository upload occurs.

The scenario requests local-only installation, so it does not exercise GitHub PR
creation. It uses `codex exec` and resume, not an interactive TUI or desktop UI.
Command sandbox/approval controls are bypassed only inside this disposable VM;
Doomer's own update and repository permissions are exercised. Hook configuration
is checked, but native hook trust and activation are not claimed by this test.
There is no coding task or review-delivery assertion yet.

## Spending and cleanup

The real OpenAI key remains in the runner. Codex gets a dummy proxy credential.
The proxy only forwards requests for the selected model to the Responses API,
forces standard service tier and a 4,096-output-token limit, and calls the
OpenAI input-token counter before reserving a conservative allowance for each
inference request. Requests that cannot fit the
remaining budget are rejected. Failed responses with missing usage retain their
reservation. The report distinguishes usage-based estimated cost from charged
budget, includes token counts, and does not imply a zero-cost failed request.

Rates used for GPT-6.1 Terra are $1/M input, $0.05/M cached input and $5/M output.
Check https://developers.openai.com/api/docs/pricing before changing the model or
running this experiment after a pricing change. The runner currently rejects
other models because their budget calculations have not been verified.
VM fees are separate from API cost.

Every invocation creates one VM and attempts deletion in a deferred cleanup,
even on failure or timeout. The VM name is printed immediately and included in
the report. A process killed with SIGKILL or a lost SSH connection can prevent
cleanup; in that case use `ssh exe.dev rm VM_NAME`. A failed cleanup makes the
run fail. Local temporary key copies and remote test homes are removed.
Each JSON report has an adjacent `.html` replay with a timeline, Play/Pause,
Previous/Next and a step slider. It records actual headless agent messages,
completed commands and results, and explicitly labels scripted user replies.
It is not a screen recording of the interactive Codex TUI. Playback compresses
long pauses to 2.5 seconds while retaining original timestamps. There are no
external scripts, tracking requests, or replay hosting requirements.

Reports contain test-agent messages and command output. Keep them private.

Codex distribution source and API contracts:

- https://github.com/openai/codex/releases/tag/rust-v0.167.0
- https://developers.openai.com/api/docs/guides/token-counting
- https://exe.dev/docs/api
