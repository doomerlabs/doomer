# Comment voice

**Voice** is how the CLI turns deterministic finding text into GitHub pull request
comments that sound like a product or persona (for example a blunt maintainer),
without hard-coding review strings in detection rules.

| Concern | Where it lives |
|---------|----------------|
| **What** is found | Package rules / model review (`src/`, specialists via `uses`) |
| **How** it sounds | Package `agent/voice.md` (+ example bank) and CLI rewrite |
| **When** to fire | Scope, train “when to post”, detection heuristics |

Voice does **not** invent technical depth. If the finding summary is empty of
mechanism, rewrite can only sound blunt about a shallow claim. Put evidence and
reasoning in the finding; put cadence and bans in voice.

## When voice runs

```sh
doomer run ./my-adversary --path ./app \
  --github-review \
  --model-provider openai --model …
```

1. Adversaries produce findings (template bodies).
2. If a model provider is configured, the CLI **rewrites** each planned comment
   with a prompt built from the resolved voice document.
3. On rewrite failure or missing credentials, the **template** body is kept.

For `terse`, the rewrite is limited to 50 words. The CLI asks the model to
rewrite once more if it exceeds that limit. If the second attempt still runs
long, a short finding template is kept. GitHub shows the code location, so
visible comments omit severity labels, adversary and package names, commit
SHAs, and repeated location headings. Tracking metadata remains in a hidden
HTML comment for review thread reconciliation and feedback.

The default is `direct` tone with `terse` length, `medium` politeness, and `high` formality. Set the controls on a run:

```sh
doomer run ./my-adversary --path ./app --github-review \
  --github-comment-tone neutral --github-comment-conciseness standard \
  --github-comment-politeness low --github-comment-formality medium
```

Accepted tones are `direct`, `neutral`, and `coaching`. Accepted lengths are
`terse` (essentials only), `standard` (enough context to act), and
`explanatory` (mechanism and impact when useful). Length controls overall depth,
not the number of sentences. For CLI environment configuration,
set `DOOMER_COMMENT_TONE`, `DOOMER_COMMENT_CONCISENESS`,
`DOOMER_COMMENT_POLITENESS`, and `DOOMER_COMMENT_FORMALITY`; explicit flags take
precedence. Hosted reviews invoke this CLI rewrite path; the hosted app passes
its project voice settings through the same flags when running a compatible CLI.
They affect wording only: the
finding, severity, confidence, and recommendation remain grounded in the
original evidence. When the evidence is incomplete, the comment states the
observed risk and the missing context rather than asserting a defect.
Low politeness is blunt about code, never personal. Low formality can use
occasional mild swearing, never slurs or abuse.
`very-low` politeness goes further: it leads with the defect and an imperative
fix, skipping niceties. It can sharply criticize the PR and say not to merge
as-is when the evidence supports a blocking issue; it never attacks the author.

Without `--github-review`, voice files are unused for posting (findings still
print to the terminal / JSON as usual).

## Resolving the voice file

First hit wins:

1. **Adversary package roots** (local path args that look like packages: have
   `adversary.yaml` or `agent/scope.md` / `docs/scope.md`), in CLI order:
   - `agent/voice.md` (preferred)
   - `train/voice.md`
   - `voice.md`
   - `VOICE.md` (legacy)
2. **Review target** (`--path`), same relative names
3. Else the **CLI-embedded** default prompt

Examples:

```sh
# Voice from torvalds package (entry), not from each specialist
doomer run ./torvalds-adversary --path ../app --github-review

# Composition: entry package still owns voice
doomer run lang/go --path ./service --github-review
# → findings from go/* members; rewrite voice from lang/go if it has agent/voice.md
```

With **composition** (`uses`), prefer putting persona voice on the package you
**name on the CLI**. Member packages’ voice files are not used for rewrite when
they are only pulled in as members. See [composition](./composition.md).

Core voice files are size-capped (~**32 KiB**) when loaded from disk.

## Package layout (`agent/voice.md`)

`doomer init` scaffolds `agent/voice.md` with:

1. **Core voice** — persona rules, length, bans, output shape
2. **Example maintainer comments (style only)** — few-shot bank with spirit
   subsections
3. **Output** — “return only the PR comment body”

### Core voice

Edit this for product tone:

- Lead with the issue; mechanism over attitude
- Confidence honesty; no invented files/APIs
- Length targets; product conciseness overrides a conflicting package target
- Hard bans (corporate padding, praise sandwiches, etc. for a Torvalds-style pack)

### Example bank (style few-shots)

Under a heading exactly:

```markdown
## Example maintainer comments (style only)
```

use subsections:

| Subsection | Spirit |
|------------|--------|
| `### Ship / OK` | Landable / LGTM-class signals |
| `### Design / technical judgment` | Brittle or wrong approach |
| `### Defects / correctness` | Real bugs / invariants |
| `### Nits / style` | Non-blocking taste |

Bank **real human** review excerpts as blockquotes (train apply issues spell this
out). Rules for the model (also enforced in the CLI rewrite preamble):

- Match **cadence and bluntness**, not copy the quote
- Re-ground every claim in the **current** finding’s evidence
- **Never** emit an example quote unchanged as the PR comment
- **Never** invent facts from examples that are not in the finding

Optional one-line source note after a quote:

```markdown
> Looks all reasonable to me
>
> _(source: https://github.com/org/repo/pull/71 — style only)_
```

### Optional section files (large corpora)

For large persona banks, packages may also keep spirit-class files next to core
voice (for example `agent/voice-ship.md`, `voice-design.md`, `voice-defects.md`,
`voice-nits.md`, plus optional `voice-ship-1.md` shards). That keeps **core rules
small** and growth in bank files.

Today the CLI rewrite loads the **core** voice path (`agent/voice.md`) as one
document (plus rewrite instructions). Keep active few-shots **inside** that file
(or under the example-bank heading) so rewrite sees them. Use separate section
files as the authoring/target layout train apply points at when packages adopt
split banks; see package READMEs (e.g. torvalds-adversary).

## Rewrite pipeline

```text
finding → template body
       → BuildRewritePrompt(voice.md + task preamble)
       → model (JSON { "body": "…" })
       → tracking marker appended
       → GitHub comment
```

The rewrite task preamble tells the model to treat the example bank as few-shot
style only. The product tone and conciseness rules are appended after package
voice rules and win when length or persona instructions conflict. JSON input
includes severity, title, template body, path/line, and
`exampleBankHint` (preferred subsection: Ship / OK, Design, Defects, or Nits).

The ten issue examples live in `internal/githubreview/testdata/voice_goldens.json`.
They are wording targets, not permission to infer a defect from a question:
the corresponding code evidence must establish each claim. The fixture rubric
is documented beside them and requires 5/5 for every locked target. The shared
prompt tests check that every tone and length combination keeps the evidence
guardrail. These offline checks do not establish whether a model output is
accurate on a real diff. For an evidence-backed model check, set
`DOOMER_VOICE_EVAL_PROVIDER` and `DOOMER_VOICE_EVAL_MODEL` plus the provider's
normal credentials, then run:

```sh
go test ./internal/githubreview -run TestVoiceGoldensWithModelJudge -count=1
```

The live judge requires at least 4/5
on every documented dimension for every fixture; see
`internal/githubreview/testdata/voice_rubric.md`.

**Model flags** (shared with analysis when configured):

- `--model-provider` (`openai` | `cloudflare` | `anthropic` | `fireworks` | `camel` | `codex`)
- `--model`

Env overrides: `ADVERSARY_MODEL_PROVIDER`, `ADVERSARY_MODEL`.

## Training: banking gold

Private catalog training retains human review evidence in its local inbox.
Accepted evidence can later teach detection and supply human-authored voice
examples through a reviewed catalog change. Do not bank synthetic draft titles
or package-generated text.

## Authoring checklist

- [ ] `agent/voice.md` exists for persona / product packages
- [ ] Core rules match the product (bans, length, structure)
- [ ] Example bank heading + spirit subsections present
- [ ] Gold is short, human, deduped
- [ ] Findings carry enough mechanism for rewrite to stay honest
- [ ] With composition, voice lives on the **entry** package you run

## Related

- [Private catalog training](../README.md#private-catalog-training) — collect and review human evidence locally
- [GitHub PR review posting](./github-review-posting.md) — flags, auth, placement
- [Composition (`uses`)](./composition.md) — entry package owns voice under multi-run
- [Automatic detection](./automatic-detection.md) — who runs; separate from voice
