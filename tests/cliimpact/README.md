# Bounded CLI dependency proof

Run from the repository root:

```sh
make cli-impact
```

The CI CLI smoke stage now runs the stronger, signed-reuse mutation proof in
`tests/codexinstall/cli_smoke_proof_test.go`. This original diagnostic remains
available through `make cli-impact`.

This diagnostic builds the actual CLI three times and runs exactly
`version --format json` in a disposable home:

1. Unmodified source: require successful JSON output.
2. Insert a panic before the `whoami` handler: require identical successful
   output. Its full source fingerprint changes; the experimental fingerprint
   that omits only that handler body stays the same.
3. Insert a panic before the `version` handler: require that panic to execute
   and the experimental fingerprint to change.

Go build overlays leave checkout source unchanged. The traps preserve the
original code after the panic so imports still compile. The experimental
fingerprint retains every captured repository file except the omitted body,
including command registration, metadata, flags, initialization, dependencies,
and other handlers. The tool exits unsuccessfully if any expectation fails.
Build and command times are included in each observation; these are not E2E
cache performance measurements.

## What this proves

For this fixed command and environment, the real command dispatcher executes
the selected handler without entering `whoami`. The mutation tests also prove
that changing selected code or shared inputs changes the experimental identity.
This is the first bounded example for product-command dependency analysis.

## What this does not prove

The output explicitly sets `cache_eligible` to `false`. The fingerprint is
source-only and is not connected to local or CI cache lookup or publication.
It is not a proof for all possible inputs, hooks, platforms or environments.

Existing agent E2E scenarios allow agents to execute arbitrary shell commands.
An observed command list cannot establish every command a later execution
might choose. Those scenarios therefore keep the existing shared product CLI
fingerprint and all current assertions.

Before narrowing their product dependencies, establish an enforceable command
boundary for each eligible scenario, include all allowed handlers and shared
startup dependencies, and fail closed when commands or callback routing are
unknown. Command aliases, direct callback invocation, nested command execution,
help/version flags, external programs and package initialization all need to
be accounted for. A manifest may add dependencies; it must not silently assert
that an unconstrained agent cannot reach other code.

## Real bounded smoke cache

The existing version/profile/login/logout smoke assertions now run through:

```sh
go run -trimpath -buildvcs=false -ldflags=-buildid= ./tests/codexinstall \
  --cli-smoke --output /tmp/cli-smoke.json
```

The smoke runner captures source before building. It first type-checks the
original command package, then builds a test-only CLI with the `whoami` handler
replaced by a panic. Its imports retain their initialization effects. Command
metadata, flags, startup, shared helpers and every other handler stay present.
Unsupported projection falls back to the full binary. Product/release binaries
and existing agent scenarios are unchanged.

The cache key includes the **actual test binary bytes**, **actual runner binary
bytes**, OS and architecture. It never uses the diagnostic source fingerprint
above. A change inside the removed handler produces identical test binary bytes;
a change to surviving behavior changes the identity. This deliberately handles
only one exclusion so far. It is not a general command call graph.

Results require the same dedicated Ed25519 verification keys as the agent cache
and expire six hours after their original execution. Failed, unsigned, tampered,
mismatched and expired results are misses. Cache hits preserve execution times
and cannot be republished to extend expiry. The signed local cache is under the
user cache directory (override with `E2E_CLI_SMOKE_CACHE_DIR`); shared results use
`v1/cli-smoke/` in the existing dedicated R2 bucket. Missing credentials cause
fresh execution. `--no-cache` bypasses lookup/publication; `--cache-only` fails
on a miss. Reports distinguish `cache_hit` from newly executed checks.

To populate CI-compatible results on a machine with Docker and Doppler access:

```sh
E2E_CLI_SMOKE_ONLY=1 E2E_LOCAL_REPORTS=/tmp/doomer-cli-smoke-linux \
  doppler run --project doomer --config test -- scripts/agent-e2e-local-linux.sh
```

CI has read-only R2 credentials; it executes the smoke checks on a miss. The
mutation proof runs independently of cache hits and verifies that a `whoami`
edit reuses the original signed success while a broken `version` edit misses
and fails. Run it directly with:

```sh
E2E_CLI_SMOKE_PROOF=1 go test ./tests/codexinstall \
  -run '^TestCLISmokeReuseProof$' -count=1 -v
```

This establishes real local/shared reuse, **not an agent-suite speedup**. The
smoke commands are already cheap, and building the keyed artifacts still costs
time. Wider reuse requires more useful, enforceable command boundaries.
