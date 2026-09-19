package modelreview

import (
	"context"
	"regexp"
	"strings"
	"sync"
)

// UsageRecord contains only model identity and token counts, never prompts,
// credentials, endpoints, outputs, or repository data. Each entry is one
// provider request so the server can apply context-length pricing correctly.
type UsageRecord struct {
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	InputTokens  *int   `json:"input_tokens"`
	OutputTokens *int   `json:"output_tokens"`
}

var usageIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*$`)

type usageKey struct{}
type usageCollector struct {
	mu      sync.Mutex
	records []UsageRecord
}

// WithUsageCollector scopes accounting to one command, including its concurrent
// reviewers, verification, and retries. There is no process-global accounting.
func WithUsageCollector(ctx context.Context) context.Context {
	return context.WithValue(ctx, usageKey{}, &usageCollector{records: make([]UsageRecord, 0)})
}

// CollectedUsage returns nil for uninstrumented callers and [] for a run that
// made no model calls. Those states must remain distinct on the wire.
func CollectedUsage(ctx context.Context) []UsageRecord {
	collector, _ := ctx.Value(usageKey{}).(*usageCollector)
	if collector == nil {
		return nil
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	out := make([]UsageRecord, len(collector.records))
	copy(out, collector.records)
	return out
}

type usageObservation struct {
	ctx             context.Context
	provider, model string
	pending         bool
}

func observeUsage(ctx context.Context, provider, model string) *usageObservation {
	return &usageObservation{ctx: ctx, provider: provider, model: model, pending: true}
}

func (o *usageObservation) finish() {
	// A failed/undecodable response may have consumed tokens. Preserve that
	// uncertainty instead of allowing a partial count to look like a full cost.
	if o.pending {
		o.record("", nil)
	}
}

func (o *usageObservation) record(model string, usage *Usage) {
	o.pending = false
	collector, _ := o.ctx.Value(usageKey{}).(*usageCollector)
	if collector == nil {
		return
	}
	if model == "" {
		model = o.model
	}
	if len(model) > 200 || !usageIdentifier.MatchString(model) || strings.Contains(model, "://") {
		model = "unknown"
	}
	record := UsageRecord{Provider: o.provider, Model: model}
	if usage != nil && usage.InputTokens >= 0 && usage.OutputTokens >= 0 && usage.InputTokens <= 1_000_000_000 && usage.OutputTokens <= 1_000_000_000 {
		input, output := usage.InputTokens, usage.OutputTokens
		record.InputTokens, record.OutputTokens = &input, &output
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.records) < 4095 {
		collector.records = append(collector.records, record)
	} else if len(collector.records) == 4095 {
		// Bound the payload and explicitly mark overflow as unreported usage.
		collector.records = append(collector.records, UsageRecord{Provider: "unknown", Model: "usage-limit-exceeded"})
	}
}
