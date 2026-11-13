---
name: agent-e2e
description: Run and diagnose Doomer's real-agent onboarding and direct-hook E2E tests when changing installation, onboarding, review, hooks, or the agent harness.
---

# Agent onboarding E2E

Read `tests/codexinstall/README.md` and `tests/codexinstall/onboarding-inputs.json`.
The suite replaces the old managed lifecycle tests. Do not restore managed
upgrade, update-policy, portable-state, or managed-task scenarios. Both pinned
agents exercise the Node manager, official skills CLI, native Go CLI and hooks.

Respect the user's local-build restriction. Agents must not execute the real
E2E helper when it builds a candidate CLI. Use the runner's unit tests while
leaving local CLI builds to the user and real-agent validation to authorized CI:

```sh
go test -race ./tests/codexinstall -skip '^TestPrunedRuntimeBuildsRealCLIAndRunner$'
```

Do not run the skipped infrastructure test: it builds a CLI binary. When a user
runs real E2E locally, use the Linux helper through Doppler `doomer/test`:

```sh
doppler run --project doomer --config test -- scripts/agent-e2e-local-linux.sh
```

Start applicable real-agent CI early, after source stabilizes. Inspect retained
selection evidence and reports before reporting which scenarios passed. Do not
claim a green real-agent suite from unit tests. Every case needs final-source
validation; no known failure becomes a pass by weakening its assertion.

`onboarding-inputs.json` pins the Node and skill commits and skills CLI version.
The snapshot includes their source plus the official skills CLI's dependency
lockfile. Local `E2E_NODE_SOURCE` / `E2E_SKILLS_SOURCE` overrides select candidate
checkouts for direct CI-helper runs. Never use the user's installed CLI or live
agent configuration as a fixture. Keep the native executable inside the
fresh disposable home. Test secrets come from Doppler or Depot; never print or
commit them, installation identities, private reports or recordings.

Preserve the real native trust path. Hook configuration is not proof of hook
activation. Review assertions must observe API submissions with session/turn
identity and explicit recorded finding feedback. Standalone reviews use the
installed Go CLI. The Node manager performs installation and configuration.

Source capture, scenario dependency selection, signed cache verification,
per-case budget limits, recordings, cleanup, and one fresh-home CI retry remain
part of the harness. Cache schema 3 excludes legacy successes. Record cache hits
separately from newly executed tests. macOS results do not satisfy Linux CI.
Do not use old baseline-policy regression jobs or old release catalogs.

Keep all PRs under an existing `/doomer no-approve` hold when requested. The
hold prevents approval/merging; it does not mean a PR must be a draft.
