---
name: agent-e2e
description: Run and diagnose the shared Doomer onboarding and direct-hook E2E tests.
---

Read the README and AGENTS.md in the sibling `agent-e2e` repository. It owns the suite, Jev impact selection, dependency projections, signed R2 cache, recordings and infrastructure tests. The Go, Node and skills workflows pin the shared runner commit.

Never build or install a product CLI locally. Use a user-supplied or CI-built native binary with the runner's `--cli-binary` or `--cli-artifact` input. The Go helper requires `E2E_CANDIDATE_BINARY` by default. Only users may explicitly enable `E2E_BUILD_CANDIDATE=1`; agents must not. Unit tests in the shared repository exclude `TestPrunedRuntimeBuildsRealCLIAndRunner` locally.

Use Doppler `doomer/test` and the pinned Linux environment for results intended for CI reuse. Preserve all consent, native trust, real review submission, session/turn identity, recorded feedback and cleanup assertions. Do not restore legacy managed-upgrade, policy or doomer-review journeys. Inspect the retained selection evidence and report cache hits separately from new executions.

Keep requested `/doomer no-approve` holds and never merge or publish.

Use disposable fixture homes only; never alter the user’s installed CLI, agent configuration or live hooks. Never print credentials or commit secrets, account identities, or private recordings. If Docker or Doppler access is unavailable, report that CI must execute the missing scenarios.
