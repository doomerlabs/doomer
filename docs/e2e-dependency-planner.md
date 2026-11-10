# Per-test dependency planner work in progress

This branch starts the follow-up to signed E2E result reuse. It does not yet
change the lifecycle runner's cache decisions or enable narrower product-code
reuse in CI.

`internal/e2eimpact` traces resolved Go declarations from supplied entry points,
includes package initialization, and produces a source footprint with an
explanation for each included file. Changing a transitive dependency changes
that test's fingerprint. A callback, interface dispatch, unavailable body,
external call, or external initialization broadens the footprint to all supplied
inputs. Invalid source fails analysis instead of producing an empty footprint.

`e2e-extra-dependencies.yml` is the one repository-root file for manual additions:

```yaml
version: 1
extra_dependencies:
  "prompts/codex/**": ["codex/*"]
  "config/review-policy.yaml": ["codex/*", "claude/*"]
```

Those paths are illustrative, not files currently declared by this repository.
Rules only add dependencies. File patterns support `**` across directories and
ordinary path globs within a directory. Unknown test selectors, unmatched file
patterns, unsupported versions, duplicate YAML keys, and unsafe paths fail
validation. Test identifiers are stable names, not positions in the suite.

The mutation tests use a real parsed/type-checked Go fixture and a fake executor
with a cache: an edit to `blargh.go` reruns A and reuses B; shared edits rerun both;
adding a new call from B makes later edits rerun B as well. They also exercise
dynamic-call fallback, package initializers, receiver-specific methods, and
manual extra dependencies. They do not invoke a model or publish shared results.

Before this can control agent E2E reuse, the remaining work is:

1. Load exactly the compiler-selected files and discover the scenario entry points
   automatically. The current API deliberately requires build-selected input and
   supplied entry points; it must not be fed an arbitrary walk of Go files.
2. Extend resolved edges across packages while retaining conservative handling of
   interface dispatch, callbacks, reflection, and generated/native code.
3. Model the process boundary between the test runner, agent, and Doomer CLI.
   Observed commands alone cannot bound what an agent might do on another run.
4. Capture file reads, embedded resources, configuration and external fixtures.
   Source fingerprints alone never authorize reuse; the existing signed-result
   and frozen execution-context checks remain required.
5. Run the planner in shadow mode, compare its proposed skips with full runs,
   inspect every false negative, then enable only justified dependency boundaries.

The full source snapshot remains useful for provenance. It need not become every
test's key, but unresolved behavior continues to require broad invalidation.
