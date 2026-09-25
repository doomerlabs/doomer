package githubreview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/doomerlabs/doomer/internal/modelreview"
	"github.com/doomerlabs/doomer/pkg/review"
)

type fakeProvider struct {
	name  string
	model string
	// bodies maps finding id -> rewrite body (JSON object without outer schema wrap handled by Review)
	bodies      map[string]string
	responses   []string
	summaryBody string
	calls       int
	fail        bool
	onReview    func(context.Context, int) error
	requests    []modelreview.Request
}

func (f *fakeProvider) Name() string  { return f.name }
func (f *fakeProvider) Model() string { return f.model }

func (f *fakeProvider) Review(ctx context.Context, req modelreview.Request) (modelreview.Result, error) {
	f.calls++
	f.requests = append(f.requests, req)
	if f.onReview != nil {
		if err := f.onReview(ctx, f.calls); err != nil {
			return modelreview.Result{}, err
		}
	}
	if f.fail {
		return modelreview.Result{}, &modelreview.ProviderError{Code: "fail", Message: "provider down"}
	}
	if strings.Contains(req.Prompt, "aggregate summary") {
		out, _ := json.Marshal(map[string]string{"body": f.summaryBody})
		return modelreview.Result{Output: out}, nil
	}
	// Rewrite prompt always wraps package/CLI voice with the CLI task preamble.
	if !strings.Contains(req.Prompt, "CLI comment rewrite task") {
		return modelreview.Result{}, &modelreview.ProviderError{Code: "bad_prompt", Message: "missing rewrite preamble"}
	}
	if !strings.Contains(req.Prompt, "Doomer") && !strings.Contains(req.Prompt, "Custom") &&
		!strings.Contains(req.Prompt, "Example maintainer comments") {
		return modelreview.Result{}, &modelreview.ProviderError{Code: "bad_prompt", Message: "missing voice document"}
	}
	var in struct {
		FindingID string `json:"findingId"`
	}
	if err := json.Unmarshal(req.Input, &in); err != nil {
		return modelreview.Result{}, err
	}
	body, ok := f.bodies[in.FindingID]
	if !ok {
		body = "Rewritten: " + in.FindingID
	}
	if len(f.responses) >= f.calls {
		body = f.responses[f.calls-1]
	}
	out, _ := json.Marshal(map[string]string{"body": body})
	return modelreview.Result{Output: out}, nil
}

func TestTerseCommentRetriesAnOverlongRewrite(t *testing.T) {
	long := strings.Repeat("This repeats the same review finding without helping the author act. ", 12)
	provider := &fakeProvider{responses: []string{long, "A resolved carried thread aborts the review. Treat its missing ID as resolved and post the finding again."}}
	plan := CommentPlan{Comments: []PlannedComment{{FindingID: "f1", Body: "template", BodySource: "template", Placement: "inline"}}}
	EnhanceBodies(context.Background(), &plan, EnhanceOptions{Provider: provider, VoicePrompt: DefaultVoicePrompt, Style: CommentStyle{Conciseness: "terse"}})
	if provider.calls != 2 || plan.Comments[0].BodySource != "llm" || !strings.Contains(plan.Comments[0].Body, "Treat its missing ID") {
		t.Fatalf("calls=%d comment=%+v", provider.calls, plan.Comments[0])
	}
	if !strings.Contains(provider.requests[1].Prompt, "Required correction") {
		t.Fatal("retry did not ask for a shorter comment")
	}
}

func TestTerseCommentRejectsRepeatedOverlongRewrite(t *testing.T) {
	long := strings.Repeat("This repeats the same review finding without helping the author act. ", 12)
	provider := &fakeProvider{responses: []string{long, long}}
	plan := CommentPlan{Comments: []PlannedComment{{FindingID: "f1", Body: "template", BodySource: "template", Placement: "inline"}}}
	var failures []CommentRewriteFailure
	EnhanceBodies(context.Background(), &plan, EnhanceOptions{Provider: provider, VoicePrompt: DefaultVoicePrompt, Style: CommentStyle{Conciseness: "terse"}, OnFailure: func(f CommentRewriteFailure) { failures = append(failures, f) }})
	if provider.calls != 2 || plan.Comments[0].Body != "template" || plan.Comments[0].BodySource != "template" {
		t.Fatalf("calls=%d comment=%+v", provider.calls, plan.Comments[0])
	}
	if len(failures) != 1 || failures[0].FindingID != "f1" || !strings.Contains(failures[0].Reason, "exceeds 50 words") || !strings.Contains(failures[0].Retry, "exceeds 50 words") {
		t.Fatalf("failure reasons: %+v", failures)
	}
}

func TestDirectCommentRetriesStockConclusion(t *testing.T) {
	provider := &fakeProvider{responses: []string{
		"The fix is to drop the carried entry. As-is this shouldn't merge.",
		"A resolved carried thread aborts the review. Drop the carried entry and post the finding again.",
	}}
	plan := CommentPlan{Comments: []PlannedComment{{FindingID: "f1", Body: "A resolved thread aborts the review.", BodySource: "template", Placement: "inline"}}}
	EnhanceBodies(context.Background(), &plan, EnhanceOptions{Provider: provider, VoicePrompt: DefaultVoicePrompt, Style: CommentStyle{Tone: "direct", Conciseness: "terse"}})
	if provider.calls != 2 || !strings.Contains(plan.Comments[0].Body, "Drop the carried entry") {
		t.Fatalf("calls=%d comment=%+v", provider.calls, plan.Comments[0])
	}
}

func TestTerseCommentRejectsVisibleReviewMetadata(t *testing.T) {
	sha := strings.Repeat("a", 40)
	adversary := "registry.doomer.ai/library/review/code@sha256:" + sha
	provider := &fakeProvider{responses: []string{
		"### [medium] " + adversary + " — Stuck work blocks submissions",
		"Stale generating rows block new submissions after builder loss. Reset them after the download timeout.",
	}}
	plan := CommentPlan{Comments: []PlannedComment{{
		FindingID: "f1", Adversary: adversary, HeadSHA: sha, Severity: "medium",
		Body:       "Stuck work blocks submissions.\n\n<!-- adversary-review:v2 adversary=secret head=" + sha + " -->",
		BodySource: "template", Placement: "inline",
	}}}
	EnhanceBodies(context.Background(), &plan, EnhanceOptions{Provider: provider, VoicePrompt: DefaultVoicePrompt, Style: CommentStyle{Conciseness: "terse"}})
	if provider.calls != 2 || plan.Comments[0].BodySource != "llm" {
		t.Fatalf("metadata rewrite was not retried: calls=%d comment=%+v", provider.calls, plan.Comments[0])
	}
	if strings.Contains(string(provider.requests[0].Input), sha) || strings.Contains(string(provider.requests[0].Input), adversary) {
		t.Fatalf("rewrite input leaked provenance: %s", provider.requests[0].Input)
	}
	visible := stripReviewMarker(plan.Comments[0].Body)
	if strings.Contains(visible, "medium") || strings.Contains(visible, adversary) || strings.Contains(visible, sha) {
		t.Fatalf("visible comment leaked metadata: %q", visible)
	}
}

func TestEnhanceSummarySynthesizesActualFindings(t *testing.T) {
	plan := CommentPlan{
		ReviewBody: "deterministic fallback",
		Comments: []PlannedComment{
			{FindingID: "one", Adversary: "ci/depot", Severity: "high", Title: "Pin actions", Body: "Pin mutable actions."},
			{FindingID: "two", Adversary: "go/security", Severity: "medium", Title: "Validate input", Body: "Validate the request."},
		},
	}
	provider := &fakeProvider{summaryBody: "Pin the privileged actions first, then validate the request boundary."}
	EnhanceSummary(context.Background(), &plan, EnhanceOptions{Provider: provider})
	if !strings.Contains(plan.ReviewBody, "Pin the privileged actions first") {
		t.Fatalf("summary = %q", plan.ReviewBody)
	}
}

func TestEnhanceBodiesUsesProviderAndSetsLLMSource(t *testing.T) {
	line := 3
	env := review.RunEnvelope{
		ProtocolVersion: 1,
		Result: review.ReviewResult{
			Adversary: review.ReviewAdversary{Name: "go-cli"},
			Positives: []review.Note{}, Observations: []review.Note{},
			Findings: []review.Finding{{
				ID: "f1", Title: "Issue", Category: "c", Severity: "high", Confidence: "high",
				Summary: "raw summary", Evidence: []review.Evidence{{File: "a.go", Line: &line}},
				Recommendation: "fix",
			}},
			Suppressed: review.Suppressed{},
		},
	}
	plan := ProjectFindings([]NamedEnvelope{{Adversary: "go-cli", Envelope: env}}, ProjectOptions{
		Voice: VoiceInfo{Source: "cli_default"},
	})
	if plan.Comments[0].BodySource != "template" {
		t.Fatalf("pre-enhance: %s", plan.Comments[0].BodySource)
	}
	fp := &fakeProvider{
		name: "fake", model: "m",
		bodies: map[string]string{"f1": "Concise rewrite of the issue."},
	}
	EnhanceBodies(context.Background(), &plan, EnhanceOptions{
		Provider:    fp,
		VoicePrompt: DefaultVoicePrompt,
	})
	if fp.calls != 1 {
		t.Fatalf("calls %d", fp.calls)
	}
	if plan.Comments[0].BodySource != "llm" {
		t.Fatalf("source %s", plan.Comments[0].BodySource)
	}
	if !strings.Contains(plan.Comments[0].Body, "Concise rewrite") {
		t.Fatal(plan.Comments[0].Body)
	}
	if !strings.Contains(plan.Comments[0].Body, "adversary-review:v2") {
		t.Fatal("marker missing after enhance")
	}
}

func TestEnhanceBodiesFallsBackOnProviderFailure(t *testing.T) {
	line := 1
	env := review.RunEnvelope{
		ProtocolVersion: 1,
		Result: review.ReviewResult{
			Adversary:    review.ReviewAdversary{Name: "x"},
			Positives:    []review.Note{},
			Observations: []review.Note{},
			Findings: []review.Finding{{
				ID: "f1", Title: "T", Category: "c", Severity: "high", Confidence: "high",
				Summary: "s", Evidence: []review.Evidence{{File: "a.go", Line: &line}},
			}},
			Suppressed: review.Suppressed{},
		},
	}
	plan := ProjectFindings([]NamedEnvelope{{Adversary: "x", Envelope: env}}, ProjectOptions{})
	before := plan.Comments[0].Body
	fp := &fakeProvider{fail: true, bodies: map[string]string{}}
	var failures []CommentRewriteFailure
	EnhanceBodies(context.Background(), &plan, EnhanceOptions{
		Provider:    fp,
		VoicePrompt: DefaultVoicePrompt,
		OnFailure:   func(f CommentRewriteFailure) { failures = append(failures, f) },
	})
	if plan.Comments[0].BodySource != "template" {
		t.Fatalf("%s", plan.Comments[0].BodySource)
	}
	if plan.Comments[0].Body != before {
		t.Fatal("template body should be preserved")
	}
	if len(failures) != 1 || failures[0].FindingID != "f1" || failures[0].Reason != "provider down" || failures[0].Retry != "" {
		t.Fatalf("failure reasons: %+v", failures)
	}
}

func TestEnhanceBodiesStopsWithoutReportingCanceledRewrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &fakeProvider{onReview: func(ctx context.Context, _ int) error {
		cancel()
		return fmt.Errorf("provider stopped: %w", ctx.Err())
	}}
	plan := CommentPlan{Comments: []PlannedComment{
		{FindingID: "first", Body: "template one", BodySource: "template", Placement: "inline"},
		{FindingID: "second", Body: "template two", BodySource: "template", Placement: "inline"},
	}}
	var failures []CommentRewriteFailure
	EnhanceBodies(ctx, &plan, EnhanceOptions{Provider: provider, VoicePrompt: DefaultVoicePrompt, OnFailure: func(f CommentRewriteFailure) { failures = append(failures, f) }})
	if provider.calls != 1 || len(failures) != 0 || plan.Comments[0].Body != "template one" || plan.Comments[1].Body != "template two" {
		t.Fatalf("calls=%d failures=%+v comments=%+v", provider.calls, failures, plan.Comments)
	}
}

func TestEnhanceBodiesStopsWithoutReportingCanceledRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	provider := &fakeProvider{
		responses: []string{strings.Repeat("Too many words in this rewrite. ", 20)},
		onReview: func(ctx context.Context, call int) error {
			if call == 2 {
				cancel()
				return fmt.Errorf("retry stopped: %w", ctx.Err())
			}
			return nil
		},
	}
	plan := CommentPlan{Comments: []PlannedComment{{FindingID: "first", Body: "template", BodySource: "template", Placement: "inline"}}}
	var failures []CommentRewriteFailure
	EnhanceBodies(ctx, &plan, EnhanceOptions{Provider: provider, VoicePrompt: DefaultVoicePrompt, Style: CommentStyle{Conciseness: "terse"}, OnFailure: func(f CommentRewriteFailure) { failures = append(failures, f) }})
	if provider.calls != 2 || len(failures) != 0 || plan.Comments[0].Body != "template" {
		t.Fatalf("calls=%d failures=%+v comment=%+v", provider.calls, failures, plan.Comments[0])
	}
}

func TestEnhanceBodiesNoopWithoutProvider(t *testing.T) {
	plan := CommentPlan{Comments: []PlannedComment{{
		FindingID: "f", Body: "template", BodySource: "template", Placement: "inline",
	}}}
	var failures []CommentRewriteFailure
	EnhanceBodies(context.Background(), &plan, EnhanceOptions{VoicePrompt: DefaultVoicePrompt, OnFailure: func(f CommentRewriteFailure) { failures = append(failures, f) }})
	if plan.Comments[0].BodySource != "template" {
		t.Fatal(plan.Comments[0].BodySource)
	}
	if len(failures) != 1 || failures[0].Reason != "model provider unavailable" {
		t.Fatalf("failure reasons: %+v", failures)
	}
}

func TestEnhanceBodiesUsesRepoVoicePrompt(t *testing.T) {
	line := 2
	env := review.RunEnvelope{
		ProtocolVersion: 1,
		Result: review.ReviewResult{
			Adversary: review.ReviewAdversary{Name: "x"},
			Positives: []review.Note{}, Observations: []review.Note{},
			Findings: []review.Finding{{
				ID: "f1", Title: "T", Category: "c", Severity: "medium", Confidence: "high",
				Summary: "s", Evidence: []review.Evidence{{File: "a.go", Line: &line}},
			}},
			Suppressed: review.Suppressed{},
		},
	}
	plan := ProjectFindings([]NamedEnvelope{{Adversary: "x", Envelope: env}}, ProjectOptions{
		Voice: VoiceInfo{Source: "package", Path: "agent/voice.md"},
	})
	fp := &fakeProvider{bodies: map[string]string{"f1": "Acme voice rewrite."}}
	EnhanceBodies(context.Background(), &plan, EnhanceOptions{
		Provider:    fp,
		VoicePrompt: "Custom Acme voice for PR comments",
	})
	if plan.Comments[0].BodySource != "llm" || !strings.Contains(plan.Comments[0].Body, "Acme voice") {
		t.Fatalf("%#v", plan.Comments[0])
	}
}
