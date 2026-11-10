# Per-test dependency planner work in progress

This branch starts the follow-up to signed E2E result reuse. Both local runs and
CI use the same captured-source runner. Explicit extra dependencies now enter
its real cache keys. Automatic product-code analysis runs in shadow mode: it
does not yet authorize narrower product-code reuse.

`internal/e2eimpact` traces resolved Go declarations across captured packages,
includes package initialization, and produces a source footprint with an
explanation for each included file. Changing a transitive dependency changes
that test's fingerprint. A callback, interface dispatch, unavailable body,
external call, or external initialization broadens the footprint to all supplied
inputs. Invalid source fails analysis instead of producing an empty footprint.
The build-aware loader asks `go list` for selected source files, embedded assets,
and export data; inactive build-tag files and unit tests are excluded. It finds
the selected command entry points automatically. Scenario-specific dispatch
still needs analysis before those common entry points can become narrower roots.

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
The selector `"*"` explicitly means every test across all harnesses; `"codex/*"`
means every Codex test. Malformed or unmatched selectors are errors.
The runner validates selectors against the full scenario inventory even when
running a subset. It retains declared files in the immutable execution snapshot.
An extra-only file is hashed for its declared tests; an existing shared input
remains shared. The manifest itself is shared, so editing rules invalidates all
tests. Invalid rules stop snapshot preparation rather than allowing a cache hit.

The mutation tests use a real parsed/type-checked Go fixture and a fake executor
with a cache: an edit to `blargh.go` reruns A and reuses B; shared edits rerun both;
adding a new call from B makes later edits rerun B as well. They also exercise
dynamic-call fallback, package initializers, receiver-specific methods, and
manual extra dependencies. Signed in-memory cache tests also prove that an extra
file change preserves an unrelated hit, invalidates the dependent scenario, and
allows a matching Linux local result to satisfy CI. They do not invoke a model
or publish shared results.

Every local/CI captured manifest includes `dependency_analysis` with `mode:
shadow`, file/fallback counts and up to five examples (or an analysis error).
The full footprint is saved beside the output as `OUTPUT.dependencies.json`,
so thousands of explanations do not inflate each signed cache record. This
diagnostic never replaces the current conservative key. For agent E2E, an agent
can choose commands dynamically, so the CLI process boundary remains shared.

Before this can control agent E2E reuse, the remaining work is:

1. Derive scenario-specific roots from the runner's dispatch while retaining
   common initialization and conservative dynamic-call handling.
2. Model the process boundary between the test runner, agent, and Doomer CLI.
   Observed commands alone cannot bound what an agent might do on another run.
3. Extend capture for implicit file reads, configuration and external fixtures.
   Source fingerprints alone never authorize reuse; the existing signed-result
   and frozen execution-context checks remain required.
4. Compare shadow analysis with full runs,
   inspect every false negative, then enable only justified dependency boundaries.

The full source snapshot remains useful for provenance. It need not become every
test's key, but unresolved behavior continues to require broad invalidation.
