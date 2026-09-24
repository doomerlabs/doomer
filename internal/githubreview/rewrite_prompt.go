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
		b.WriteString("Use direct, precise, evidence-based language that assumes competence. No irritation or hostility. ")
	}
	fmt.Fprintf(&b, "Conciseness: %s. ", style.Conciseness)
	switch style.Conciseness {
	case "standard":
		b.WriteString("Use one or two short sentences; include impact when it helps explain the fix.\n")
	case "explanatory":
		b.WriteString("Use up to three short sentences; include the mechanism or consequence when supported by evidence.\n")
	default:
		b.WriteString("Aim for one line; use a second short sentence only when the fix is unclear.\n")
	}
	fmt.Fprintf(&b, "Politeness: %s. ", style.Politeness)
	switch style.Politeness {
	case "high":
		b.WriteString("Be considerate and courteous while remaining clear about the issue and fix. ")
	case "low":
		b.WriteString("Be blunt and unvarnished about the code, without insults, contempt, or attacks on the author. ")
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
