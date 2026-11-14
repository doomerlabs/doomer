---
name: agent-e2e
description: Run and diagnose the shared Doomer onboarding and direct-hook E2E tests.
---

Read the README and AGENTS.md in the sibling `agent-e2e` repository. It owns the suite, Jev impact selection, dependency projections, signed R2 cache, recordings and infrastructure tests. The Go, Node and skills workflows pin the shared runner commit.

Agents may run `make build` and automated tests, including E2E tests in isolated
environments. Do not run `make dev`, replace installed binaries, change live
configuration, start services in the user's development environment, or stop the
user's running instances. Agents may start and stop processes and containers they
create solely for tests. Use an explicit precompiled test candidate
with `--cli-binary` or `--cli-artifact` for reproducible runs. Preserve the
repository's test instructions and report cache hits separately from execution.

Use Doppler `doomer/test` and the pinned Linux environment for results intended for CI reuse. Preserve all consent, native trust, real review submission, session/turn identity, recorded feedback and cleanup assertions. Do not restore legacy managed-upgrade or doomer-review journeys. Current Node reviewer-policy scenarios are part of this suite. Inspect the retained selection evidence and report cache hits separately from new executions.

Keep requested `/doomer no-approve` holds and never merge or publish.

Use disposable fixture homes only; never alter the user’s installed CLI, agent configuration or live hooks. Never print credentials or commit secrets, account identities, or private recordings. If Docker or Doppler access is unavailable, report that CI must execute the missing scenarios.
