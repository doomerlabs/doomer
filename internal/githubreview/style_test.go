package githubreview

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestStyleChangesPromptWithoutChangingFindingInput(t *testing.T) {
	original := PlannedComment{FindingID: "race", Severity: "medium", Confidence: "low", Title: "Potential shared-state race", Body: "The map is shared across requests. Confirm whether writes are synchronized.", Placement: "inline"}
	styles := []CommentStyle{{Tone: "direct", Conciseness: "terse"}, {Tone: "coaching", Conciseness: "explanatory"}}
	var inputs [][]byte
	var prompts []string
	for _, style := range styles {
		plan := CommentPlan{Comments: []PlannedComment{original}}
		provider := &fakeProvider{bodies: map[string]string{"race": "The map appears shared across requests; confirm synchronization before assuming writes are safe."}}
		EnhanceBodies(context.Background(), &plan, EnhanceOptions{Provider: provider, VoicePrompt: DefaultVoicePrompt, Style: style})
		if len(provider.requests) != 1 || plan.Comments[0].BodySource != "llm" {
			t.Fatalf("rewrite failed for %+v", style)
		}
		inputs = append(inputs, provider.requests[0].Input)
		prompts = append(prompts, provider.requests[0].Prompt)
	}
	if !reflect.DeepEqual(inputs[0], inputs[1]) {
		t.Fatalf("style changed finding input:\n%s\n%s", inputs[0], inputs[1])
	}
	if prompts[0] == prompts[1] || !strings.Contains(prompts[0], "Tone: direct") || !strings.Contains(prompts[1], "Tone: coaching") {
		t.Fatal("style controls did not change the rewrite prompt")
	}
}

func TestCommentStyleSettings(t *testing.T) {
	def, err := (CommentStyle{}).Normalize()
	if err != nil || def.Tone != "direct" || def.Conciseness != "terse" || def.Politeness != "medium" || def.Formality != "high" {
		t.Fatalf("default = %+v, %v", def, err)
	}
	for _, tone := range []string{"direct", "neutral", "coaching"} {
		for _, length := range []string{"terse", "standard", "explanatory"} {
			style, err := (CommentStyle{Tone: tone, Conciseness: length}).Normalize()
			if err != nil {
				t.Fatal(err)
			}
			prompt := BuildRewritePromptWithStyle("# Custom voice\n\nBe verbose.", style)
			for _, want := range []string{"Tone: " + tone, "Conciseness: " + length, "Preserve the finding's meaning, urgency, confidence", "Never manufacture certainty", "not a reusable preamble", "override conflicting package style rules"} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("%s/%s: missing %q", tone, length, want)
				}
			}
			if !strings.Contains(strings.ToLower(prompt), "do not enforce a sentence count") || strings.Contains(prompt, "short sentences") || strings.Contains(prompt, "one line") {
				t.Fatalf("%s/%s: length instruction imposes a sentence or line count", tone, length)
			}
		}
	}
	for _, style := range []CommentStyle{{Tone: "hostile"}, {Conciseness: "essay"}, {Politeness: "rude"}, {Formality: "profane"}} {
		if _, err := style.Normalize(); err == nil {
			t.Fatalf("accepted invalid style %+v", style)
		}
	}
	for _, politeness := range []string{"very-low", "low", "medium", "high"} {
		for _, formality := range []string{"low", "medium", "high"} {
			style, err := (CommentStyle{Politeness: politeness, Formality: formality}).Normalize()
			if err != nil {
				t.Fatal(err)
			}
			prompt := BuildRewritePromptWithStyle("", style)
			if !strings.Contains(prompt, "Politeness: "+politeness) || !strings.Contains(prompt, "Formality: "+formality) {
				t.Fatalf("missing manner in prompt: %s", prompt)
			}
		}
	}
	cutting := BuildRewritePromptWithStyle("", CommentStyle{Politeness: "very-low"})
	for _, boundary := range []string{"imperative fix", "not a recurring catchphrase", "Criticize the PR sharply", "Never attack or ridicule the author", "only when the finding explicitly recommends blocking"} {
		if !strings.Contains(cutting, boundary) {
			t.Fatalf("cutting style missing boundary %q", boundary)
		}
	}
	human := BuildRewritePromptWithStyle("", CommentStyle{
		Tone: "direct", Conciseness: "terse", Politeness: "high", Formality: "high",
	})
	for _, want := range []string{
		"maintainer leaving an inline PR comment",
		`Do not add mini-headings or labels such as "Why this matters", "Why this bites"`,
		"Avoid stock transitions such as 'The fix is to ...'",
		"Use no more than 50 words",
		"Do not restate the same defect",
		"Courtesy does not require 'please', 'we should', 'could we'",
	} {
		if !strings.Contains(human, want) {
			t.Fatalf("direct/terse/high/high style missing human-comment guidance %q", want)
		}
	}
}

func TestVoiceGoldensAreShortDirectStatements(t *testing.T) {
	raw, err := os.ReadFile("testdata/voice_goldens.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Human  string `json:"human"`
		Target string `json:"target"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 10 {
		t.Fatalf("want 10 issue fixtures, got %d", len(fixtures))
	}
	for _, fixture := range fixtures {
		if fixture.Human == "" || fixture.Target == "" {
			t.Fatalf("empty golden: %+v", fixture)
		}
		if score := scoreDirectGolden(fixture.Target); score != 5 {
			t.Fatalf("golden scored %d/5: %q", score, fixture.Target)
		}
	}
}

func scoreDirectGolden(body string) int {
	score := 0
	lower := strings.ToLower(body)
	if len(body) <= 140 && !strings.Contains(body, "\n") {
		score++
	}
	if !containsAny(lower, "maybe", "perhaps", "just curious", "i think") {
		score++
	}
	if parts := strings.SplitN(body, " — ", 2); len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		score++
	}
	if !strings.Contains(body, "?") {
		score++
	}
	if !containsAny(lower, "hey,", "happy to discuss", "let me know", "stupid", "idiot", "lol", "😂") {
		score++
	}
	return score
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
