# Direct voice regression rubric

The ten locked targets in `voice_goldens.json` are phrasing examples. They assume
the finding evidence proves the claim; a real rewrite must retain the source
finding's confidence and must never turn a question alone into a defect.

The deterministic fixture judge in `style_test.go` requires every target to
score 5/5:

1. Concise: at most 140 characters and one line.
2. Direct: no uncertainty filler (`maybe`, `perhaps`, `just curious`, `I think`).
3. Actionable: the target names an action after the finding.
4. Statement: no question mark when the fixture assumes a proven fix.
5. Respectful: no greeting, closer, personal attack, meme, or emoji.

This rubric guards the locked targets and prompt instructions in offline CI.
It cannot judge whether a generated claim is supported by a real diff. Keep
sample-diff and model evaluations separate from this deterministic gate.

`TestVoiceGoldensWithModelJudge` is the live generation gate. CI can enable it
with `DOOMER_VOICE_EVAL_PROVIDER`, `DOOMER_VOICE_EVAL_MODEL`, and the provider's
normal credentials. It generates one comment per evidence-backed fixture, then
scores concise, direct, actionable, statement, respectful, and technical
fidelity from 1 to 5. Every dimension must score at least 4 for every fixture.
The failure prints the fixture number, score, judge reason, and generated text.
Pin the provider and model in CI; update this threshold only with a reviewed
fixture or judge change. The live test skips when both eval variables are absent.
