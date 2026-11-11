# Per-test dependency planning

This branch starts the follow-up to signed E2E result reuse. Both local runs and
CI use the same captured-source runner. Automatically discovered scenario-specific runner/helper bodies and explicit
extra dependencies enter its real cache keys. General product-code analysis
remains in shadow mode: the CLI is still shared.

`internal/e2eimpact` traces resolved Go declarations across captured packages,
includes package initialization, and produces a source footprint with an
explanation for each included file. Changing a transitive dependency changes
that test's fingerprint. A callback, interface dispatch, unavailable body,
external call, or external initialization broadens the footprint to all supplied
inputs. Invalid source fails analysis instead of producing an empty footprint.
The build-aware loader asks `go list` for selected source files, embedded assets,
and export data; inactive build-tag files and unit tests are excluded. It finds
the selected command entry points automatically. The runner separately follows its scenario dispatchers and function references
to compute executable per-scenario projections, with typed checks protecting
scenario identity. This runtime selection has no manual file-to-test list.

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

Automatic runner projections now control E2E reuse in both local runs and CI.
They preserve declarations, global initialization, compiler directives, and
escaped callbacks. Typed checks reject scenario mutation, unsafe aliases and
external writes. Unsupported identity flow falls back to full runner inputs.
Explicit extras always retain a whole file, overriding an automatic exclusion.

Real-repository mutation tests edit `reviewPreflight` and prove that install,
upgrade and policy-recovery retain their keys, then edit CLI code and prove
that every scenario invalidates. Signed-cache tests prove the same selective
acceptance in both local and CI environments. The historical manual scope
implementation is retained only in tests as a comparison baseline.

Further narrowing of product CLI paths requires a justified bound on which
commands an agent might choose. An observed successful command trace alone is
not that bound. Until one exists, all CLI code remains a dependency and the
full source inventory remains useful for provenance.

## Model selection experiment

The `model selection experiment` CI job asks `typesafe/jev` through Concentrate
for the probability that each test's assertions could be affected by the PR
diff. Its input includes the current scenario implementations and changed
source. The default threshold is 0.5 and can be set with `--threshold`. Every
probability, the threshold, and the resolved model version are recorded. New
scenarios are always included. Changes to `main.go`, `cmd/root.go`, module files
or the extra dependency manifest require the full catalog. Missing credentials,
unsupported inputs and invalid responses produce a full-catalog recommendation.

All lifecycle scenarios run through their existing workflow. After completion,
`compare model selection with full results` records failures outside the
recommendation and missing results. Those failures need investigation for
product regressions, provider errors and flakes before estimating selection
quality. Passing runs alone provide limited evidence about missed regressions.

The Concentrate secret is scoped to this repository, the `agent-e2e` environment
and `agent-e2e.yml`. Requests contain repository source and diffs. The client
uses the long-context header for the captured source context. Artifacts retain
recommendations, probabilities, input hashes and usage metadata. The threshold
is experimental and needs validation against observed failures.

Run the predictor locally from a checkout with both revisions available:

```sh
doppler run --project doomer --config test -- \
  python3 scripts/agent-e2e-selector.py predict \
  --base origin/main --head HEAD --output /tmp/e2e-selection
```

Compare with the combined CI artifact layout:

```sh
python3 scripts/agent-e2e-selector.py compare \
  --prediction /tmp/e2e-selection/prediction.json \
  --results /path/to/e2e-artifacts --output /tmp/e2e-selection
```
