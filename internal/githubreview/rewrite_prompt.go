package githubreview

import (
	"fmt"
	"strings"
)

// VoiceExampleBankHeading is the section train apply / package voice stubs use
// for few-shot maintainer quotes. Must stay aligned with train/results voice bank.
const VoiceExampleBankHeading = "## Example maintainer comments (style only)"

// rewriteTaskPreamble is prepended to package/CLI voice markdown so models
// always treat the example bank as few-shot style, not copy-paste targets.
const rewriteTaskPreamble = `# CLI comment rewrite task

You rewrite **one** automated code-review finding into a single GitHub pull request
comment body (Markdown).

## Package voice document

Everything after the separator is the package (or CLI default) voice file—typically
` + "`agent/voice.md`" + `. Follow its core voice, length, bans, and output rules.

## Example bank (few-shot style only)

If the voice document contains a section titled:

` + "`" + VoiceExampleBankHeading + "`" + `

then treat the blockquotes under it as **style few-shots**:

1. Match cadence, bluntness, and structure from examples in the subsection that best
   fits this finding (Ship / OK, Design / technical judgment, Defects / correctness,
   Nits / style)—use severity and title/summary as hints.
2. **Do not** copy any example quote unchanged as the comment body.
3. **Do not** invent technical facts from examples that are not present in the
   finding input (title, templateBody, path, line).
4. Re-ground every claim in the finding evidence in the JSON input.
5. If the example bank is empty or missing, follow core voice only.

## Rules for every voice

Change presentation only. Preserve the finding, severity, confidence, and
recommended action. Do not add a defect, stronger causal claim, or fix that
the finding evidence does not support. Be definitive when the evidence proves
the finding. When context is missing, state the observed risk and what
information would resolve it. Never manufacture certainty to sound direct.
Lead with the finding, use one idea per comment, and omit greetings, closers,
personal attacks, forced jokes, slang, and fake enthusiasm. Prefer a statement
when the finding and fix are clear; a question is allowed when information is
actually needed.
Start with the specific finding or requested fix, not a reusable preamble.
Avoid stock openers such as "Please address this", "Please take a look",
"Quick note", and "Heads up" in every tone and politeness setting.
Write like a maintainer leaving an inline PR comment, not a report generated
from a review template. Use plain, natural sentences and short paragraphs.
Do not add mini-headings or labels such as "Why this matters", "Why this bites",
"Impact", or "Fix". Avoid strained slang and euphemisms. If a sentence packs in
the mechanism, an example, the consequence, historical behavior, and the fix,
split it and remove anything the author does not need in order to act.
Do not narrate the full call chain when naming the failure and its consequence
is enough. Do not restate the same defect in a second paragraph. Omit a closing
merge verdict unless the finding explicitly calls for blocking the merge.

## JSON input fields

- findingId, adversary, severity, confidence, title
- path, line, endLine (anchor)
- templateBody (deterministic draft to rewrite)
- exampleBankHint (preferred example-bank subsection: Ship / OK, Design / technical judgment, Defects / correctness, or Nits / style)

Return only schema-valid JSON with a single "body" string (the PR comment).
`

// HasVoiceExampleBank reports whether voice markdown includes the train gold bank.
func HasVoiceExampleBank(voiceMarkdown string) bool {
	return strings.Contains(voiceMarkdown, VoiceExampleBankHeading)
}

// BuildRewritePrompt wraps package/CLI voice markdown with explicit rewrite
// instructions so agent/voice.md example banks are used when generating comments.
func BuildRewritePrompt(voiceMarkdown string) string {
	return BuildRewritePromptWithStyle(voiceMarkdown, CommentStyle{})
}

// BuildRewritePromptWithStyle applies product controls after the package voice
// document so every entry package shares the same final presentation policy.
func BuildRewritePromptWithStyle(voiceMarkdown string, style CommentStyle) string {
	var err error
	style, err = style.Normalize()
	if err != nil {
		style, _ = (CommentStyle{}).Normalize()
	}
	voice := strings.TrimSpace(voiceMarkdown)
	if voice == "" {
		voice = strings.TrimSpace(DefaultVoicePrompt)
	}
	var b strings.Builder
	b.WriteString(rewriteTaskPreamble)
	b.WriteString("\n---\n\n")
	b.WriteString(voice)
	if !strings.HasSuffix(voice, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("\n## Product comment settings (override conflicting package style rules)\n\n")
	fmt.Fprintf(&b, "Tone: %s. ", style.Tone)
	switch style.Tone {
	case "neutral":
		b.WriteString("Use plain professional language without a persona or impatience. ")
	case "coaching":
		b.WriteString("Use a warm, collegial phrasing without praise, pep talk, or hedging a clear finding. ")
	default:
		b.WriteString("Use direct, precise, evidence-based language that assumes competence. Lead with the broken behavior, then give a concrete action. Use an imperative when the fix is clear. Avoid stock transitions such as 'The fix is to ...' and do not repeat the finding as a concluding opinion. No irritation or hostility. ")
	}
	fmt.Fprintf(&b, "Conciseness: %s. ", style.Conciseness)
	switch style.Conciseness {
	case "standard":
		b.WriteString("Use enough space to state the finding, its impact when useful, and an actionable fix. Keep the overall comment focused; do not enforce a sentence count.\n")
	case "explanatory":
		b.WriteString("Explain the mechanism, evidence-backed consequence, and fix when they help the reviewer act. Let the complexity of the finding determine overall length; avoid repetition and unsupported detail. Do not enforce a sentence count.\n")
	default:
		b.WriteString("Use no more than 85 words. Preserve the specific defect, its concrete consequence when needed, and the fix. Cut background, repeated code paths, contract history, and exhaustive evidence. Prefer one short paragraph. If the issue needs more space to remain accurate, prioritize the finding and action; never pad with a merge verdict. Do not enforce a sentence count.\n")
	}
	fmt.Fprintf(&b, "Politeness: %s. ", style.Politeness)
	switch style.Politeness {
	case "high":
		b.WriteString("Be considerate and courteous while remaining clear and authoritative about the issue and fix. Courtesy does not require 'please', 'we should', 'could we', or other passive softening when the action is known. ")
	case "low":
		b.WriteString("Be blunt and unvarnished about the code, without insults, contempt, or attacks on the author. ")
	case "very-low":
		b.WriteString("Be cuttingly direct about the code and PR: lead with the defect or consequence, omit niceties and hedging, and give an imperative fix. Start with the specific issue, not a recurring catchphrase or stock opener. Criticize the PR sharply when the evidence warrants it, but give a merge verdict only when the finding explicitly recommends blocking it. Never attack or ridicule the author or imply they are incompetent; no slurs or personal abuse. ")
	default:
		b.WriteString("Be straightforward and respectful without unnecessary softening. ")
	}
	fmt.Fprintf(&b, "Formality: %s. ", style.Formality)
	switch style.Formality {
	case "low":
		b.WriteString("Use natural, casual developer language. Occasional mild swearing is allowed when it fits, never directed at a person; avoid slurs and abusive language.\n")
	case "medium":
		b.WriteString("Use conversational language without profanity.\n")
	default:
		b.WriteString("Use polished professional language without profanity.\n")
	}
	return b.String()
}
