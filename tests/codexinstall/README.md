# Manual Codex lifecycle experiments

Run real Codex CLI 0.167.0 with GPT-6.1 Terra at low reasoning in disposable
Linux exe.dev VMs. These tests are opt-in. They are never run by normal PR CI.
The local fixture supplies Doomer account, approval and review responses.
Installation still uses the public guide and published, verified Doomer runtime.

## Run

From the repository root, with Go and OpenSSH tools installed, provide
`TEST_OPENAI_API_KEY` and `TEST_EXE_SSH_PRIVATE_KEY` through Doppler `doomer/test`.
The SSH public key must be registered in exe.dev.

```sh
doppler run --project doomer --config test -- \
  go run ./tests/codexinstall --scenario first-review --budget 0.20 \
  --output /tmp/codex-first-review.json
```

List scenarios without credentials, network access or model calls.

```sh
go run ./tests/codexinstall --scenario list
```

| Scenario | Assertions |
| --- | --- |
| `install` | Both skill files, launcher, hooks configured, authenticated readiness, one grant, no content uploads |
| `first-review` | New coding task completes an account-linked review and records a finding response |
| `new-chat` | Two fresh coding chats produce separate completed reviews with the original approval |
| `worktrees` | Concurrent branches/worktrees keep files, tasks and reviews separate while sharing approval |
| `decline-updates` | Refusing updates does not grant repository access or upload content |
| `decline-uploads` | Refusing repository access does not upload content |
| `interrupted` | A new Codex process/chat resumes pending setup using its saved task and original grant request |
| `upgrade` | Historical skill and signed runtime fixture upgrades with preserved policy/grant and one simulated PR |
| `upgrade-defer` | Deferring the upgrade leaves files alone and suppresses a repeat offer on preparation |
| `service-failure` | Submission failure terminates with durable state and recovers the same task when the service returns |

Each invocation performs real installation first in its own fresh home and VM.
A failed assertion remains a failed test even when it exposes a product gap.
The upgrade fixture uses a local bare Git remote and a fake `gh` command; it
tests the agent's PR workflow without creating a real external pull request.
The interruption case tests process replacement between calls, not SIGKILL
inside an in-flight upload. These are headless exec/resume tests. Interactive
TUI, desktop UI, native hook trust and hook activation are not covered.
Command approval controls are bypassed inside the disposable VM only; Doomer's
own update and upload permissions are exercised.

## Free tests

```sh
go test -race ./tests/codexinstall
```

These test the service fixture, response parser, budget enforcement, redaction,
and HTML escaping without any paid requests or VMs.

## Reports and recordings

Each run writes a private JSON report plus an adjacent `.html` chat replay.
The replay includes actual messages, completed commands/results, timestamps,
and labeled scripted replies. It supports replay, scrubbing and expandable
command groups. It starts with the entire conversation visible. No external
scripts or replay hosting are required.

Regenerate the HTML without a model call or VM.

```sh
go run ./tests/codexinstall --render-report /tmp/codex-first-review.json
```

## Spending and cleanup

`--budget` caps one invocation. The real OpenAI key stays in the runner; Codex
receives a dummy proxy credential. Before every inference request the proxy
counts input tokens, reserves a conservative allowance, forces standard service
tier and limits output to 4,096 tokens. Requests that cannot fit are rejected.
Unknown usage retains its reservation, including a lost remote worker connection.
The JSON distinguishes measured token-based cost estimates from charged budget.

GPT-6.1 Terra rates used are $1/M input, $0.05/M cached input and $5/M output.
Check the current pricing before running after a price change. Other models
are rejected because their budgeting has not been verified. VM fees are separate.

Every invocation attempts VM deletion on completion, failure or timeout. The
name is printed immediately and included in the report. SIGKILL or lost access
can prevent cleanup; use `ssh exe.dev rm VM_NAME` if that happens. Cleanup
failure makes the test fail. Temporary private-key files and test homes are
removed. Reports include test transcripts and should remain private.

Sources

- https://github.com/openai/codex/releases/tag/rust-v0.167.0
- https://developers.openai.com/api/docs/guides/token-counting
- https://developers.openai.com/api/docs/pricing
- https://exe.dev/docs/api
