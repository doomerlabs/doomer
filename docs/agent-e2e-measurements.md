# Local agent E2E concurrency measurements

These are historical measurements of the retired managed-lifecycle suite. The
commands below are not instructions for the replacement onboarding suite; see
`tests/codexinstall/README.md` for its current scenarios and commands.

Phase 1 adds bounded local suite execution without changing the Depot matrix,
release gates, scenario assertions, authentication modes or retries. The default
remains one worker; four workers are an explicit opt-in until broader evidence
supports changing the default.

## Method

Measured on November 9, 2026, on macOS arm64. Both invocations used the same
compiled runner, CLI source, committed bootstrap inputs, scenario order and
pinned Codex 0.167.0 package with `gpt-6.1-terra` at low reasoning and API auth.
The candidate/public installation baseline was `2026.11.9.3`.

- Base revision: `abdf801` plus this PR's implementation.
- Runtime source manifest SHA-256:
  `85020079458f94b06dd3d6f8e8cd352e2d7ddc3de8777d45b16e0ceffb10441d`.
- Runner executable SHA-256:
  `e2904d9a4f3a2b12ff02aebd864674f9f6a8e00e1d5c1bec6b453da57801f199`.
- Codex package SHA-256 (verified against the GitHub release asset digest):
  `af45ed45d940d7998809622c1c0296d3098ed7d7fad695284422c228bd454517`.

The source manifest is a sorted list of SHA-256 byte hashes and paths from
tracked `cmd/`, `internal/`, `pkg/`, `tests/codexinstall/`, `main.go`, `go.mod`,
`go.sum`, and `.agents/skills/doomer-review/`, excluding `*_test.go` and
`README.md`. This is measurement provenance, not a cache identity or immutable
snapshot implementation. Runtime source was unchanged between runs.

Common invocation, run first with `--jobs 1`, then with `--jobs 4`, each using a
new output prefix:

```sh
doppler run --project doomer --config test -- /path/to/runner \
  --scenario all --suite-scenarios first-review,upgrade,policy-installed,tui \
  --jobs JOBS --local-codex /path/to/pinned/codex \
  --local-root /path/to/disposable-homes \
  --candidate-release 2026.11.9.3 --bootstrap-source . \
  --budget 0.50 --suite-budget 2 --output /path/to/unique-report.json
```

Wall time includes setup, per-scenario candidate builds, all agent turns, report
writing and cleanup. No local retries are performed. This sample covers ordinary
review, upgrade, policy and a real terminal; it is not the full suite or evidence
of Linux/Claude parity. Provider behavior and download caches remain external
sources of variation.

## Results

| Measurement | One worker | Four workers |
| --- | --- | --- |
| Wall time | 479.9s (8m00s) | 344.0s (5m44s) |
| Passed / failed / unstarted | 4 / 0 / 0 | 4 / 0 / 0 |
| Provider HTTP errors / retries | 0 / 0 | 0 / 0 |
| Cleaned disposable homes | 4 / 4 | 4 / 4 |

| Scenario | One worker | Four workers |
| --- | --- | --- |
| `first-review` | 95.4s | 302.1s |
| `upgrade` | 136.3s | 343.9s |
| `policy-installed` | 132.1s | 254.3s |
| `tui` | 116.1s | 259.6s |

The same assertion-key sets were present for each matching scenario, and every
assertion passed. Four workers reduced total wall time by 28.3% (1.40x overall
speedup), while individual scenarios took substantially longer. This small,
single-host sample does not establish provider capacity for the full suite or
Claude. Keep the default at one worker and make four workers opt-in; evaluate
broader runs before changing the default. No retries were added or needed.

These measurements used the isolated phase 1 implementation before snapshot
and cache work. Phase 2 has separate cold/cache validation recorded in the PR.
The source hash above identifies that measured phase 1 checkpoint, rather than
the expanded PR's final source.

Private JSON reports,
HTML conversations, runner logs and the source manifest are retained outside
Git at `/private/tmp/doomer-phase1-measurements/` on the measurement host.

## Incremental reuse proof

Measured on November 9, 2026, using the Linux amd64 local helper on the same
macOS host, source `a308bdd`, baseline `2026.11.9.5`, Codex API authentication
and Claude subscription authentication. A clean run executed `install` and
`review-preflight` for both agents. A separate checkout then changed only the
body of `reviewPreflight` (a harmless `_ = 0`) and appended documentation to
`AGENTS.md`. No probe changes were included in the PR.

| Scenario | First execution | Changed checkout | Changed checkout result |
| --- | --- | --- | --- |
| Codex install | 201.0s | 8.5s | Signed cache hit |
| Claude install | 117.9s | 8.2s | Signed cache hit |
| Codex review-preflight | 233.1s | 166.2s | Executed and passed |
| Claude review-preflight | 144.5s | 108.0s | Executed and passed |

The install fingerprints stayed identical; both preflight fingerprints changed.
Reused reports retained the original execution commit and timestamp. The
historical missing-policy regression witness also remained a cache hit. The
changed-checkout invocation completed in 209.0 seconds, including setup and
verification, with two reused scenarios and two newly executed scenarios.

The first invocation took 791.0 seconds including initial agent downloads and
the uncached regression witness. These are different workloads, so that total
is not a controlled speedup comparison. Downloaded agent archives are reused
only after verification against their pinned SHA-256 digest.

This proves the audited fixture boundary and documentation exclusion. Shared
CLI or runner changes still invalidate all affected inputs conservatively; this
does not claim arbitrary product code-path coverage analysis. Private reports
are retained at `/private/tmp/doomer-incremental-base-v3/` and
`/private/tmp/doomer-incremental-variant/`; the fingerprint comparison is at
`/private/tmp/pr134-incremental-proof.json` on the measurement host.
