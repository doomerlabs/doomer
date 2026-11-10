# Bounded CLI dependency proof

Run from the repository root:

```sh
make cli-impact
```

The same proof runs in the existing CI CLI smoke stage via
`scripts/ci-verify.sh smoke`.

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
