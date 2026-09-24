package githubreview

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/doomerlabs/doomer/internal/modelreview"
)

const voiceJudgeSchema = `{"type":"object","additionalProperties":false,"required":["concise","direct","actionable","statement","respectful","fidelity","reason"],"properties":{"concise":{"type":"integer","minimum":1,"maximum":5},"direct":{"type":"integer","minimum":1,"maximum":5},"actionable":{"type":"integer","minimum":1,"maximum":5},"statement":{"type":"integer","minimum":1,"maximum":5},"respectful":{"type":"integer","minimum":1,"maximum":5},"fidelity":{"type":"integer","minimum":1,"maximum":5},"reason":{"type":"string"}}}`

const voiceJudgePrompt = `Judge one generated PR comment against the supplied evidence and locked target.
Score each dimension 1 (poor) to 5 (excellent):
- concise: one short line when possible, at most two short sentences;
- direct: leads with an observed finding, no filler or unsupported certainty;
- actionable: gives a concrete correction when evidence supports one;
- statement: uses a statement rather than a question when the finding and fix are clear;
- respectful: no cringe, openers, closers, personal attacks, or fake enthusiasm;
- fidelity: preserves the technical claim and action of the locked target without adding an unsupported claim.
The locked target is a semantic reference, not a string-match requirement. Penalize a comment that asserts more than the evidence proves. Return only schema-valid JSON with scores and a brief reason.`

// This test is opt-in so ordinary offline tests stay deterministic. CI enables it
// by setting both DOOMER_VOICE_EVAL_PROVIDER and DOOMER_VOICE_EVAL_MODEL plus
// the provider's normal credentials.
func TestVoiceGoldensWithModelJudge(t *testing.T) {
	providerName := strings.TrimSpace(os.Getenv("DOOMER_VOICE_EVAL_PROVIDER"))
	modelName := strings.TrimSpace(os.Getenv("DOOMER_VOICE_EVAL_MODEL"))
	if providerName == "" && modelName == "" {
		t.Skip("set DOOMER_VOICE_EVAL_PROVIDER and DOOMER_VOICE_EVAL_MODEL to run the live voice judge")
	}
	if providerName == "" || modelName == "" {
		t.Fatal("both DOOMER_VOICE_EVAL_PROVIDER and DOOMER_VOICE_EVAL_MODEL are required")
	}
	provider, err := modelreview.ProviderFromConfig(modelreview.Config{Provider: providerName, Model: modelName}, os.LookupEnv, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/voice_goldens.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Human    string `json:"human"`
		Evidence string `json:"evidence"`
		Target   string `json:"target"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for i, fixture := range fixtures {
		if fixture.Evidence == "" {
			t.Fatalf("fixture %d has no evidence", i+1)
		}
		body, err := rewriteOne(context.Background(), provider,
			BuildRewritePromptWithStyle(DefaultVoicePrompt, CommentStyle{}),
			PlannedComment{FindingID: fixture.Human, Title: fixture.Human, Severity: "medium", Confidence: "high", Body: fixture.Evidence},
			json.RawMessage(bodyOutputSchema), 45*time.Second)
		if err != nil {
			t.Fatalf("fixture %d rewrite: %v", i+1, err)
		}
		judgeInput, _ := json.Marshal(map[string]string{"evidence": fixture.Evidence, "target": fixture.Target, "comment": body})
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		result, judgeErr := provider.Review(ctx, modelreview.Request{
			ProtocolVersion: modelreview.ProtocolVersion,
			Prompt:          voiceJudgePrompt,
			Input:           judgeInput,
			Schema:          json.RawMessage(voiceJudgeSchema),
			Budget:          modelreview.Budget{MaximumOutputTokens: 256, TimeoutMS: 45000},
		})
		cancel()
		if judgeErr != nil {
			t.Fatalf("fixture %d judge: %v", i+1, judgeErr)
		}
		var score struct {
			Concise    int    `json:"concise"`
			Direct     int    `json:"direct"`
			Actionable int    `json:"actionable"`
			Statement  int    `json:"statement"`
			Respectful int    `json:"respectful"`
			Fidelity   int    `json:"fidelity"`
			Reason     string `json:"reason"`
		}
		if err := json.Unmarshal(result.Output, &score); err != nil {
			t.Fatalf("fixture %d decode judge: %v", i+1, err)
		}
		if score.Concise < 4 || score.Direct < 4 || score.Actionable < 4 || score.Statement < 4 || score.Respectful < 4 || score.Fidelity < 4 {
			t.Errorf("fixture %d scored below 4/5: %+v; generated comment: %q", i+1, score, body)
		}
	}
}
