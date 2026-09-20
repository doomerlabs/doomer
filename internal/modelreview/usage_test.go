package modelreview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestUsageCollectorCapturesConcurrentRequestsAndResolvedModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"model":"gpt-5.6-luna-2026-07-09","output":[{"content":[{"type":"output_text","text":"{\"decision\":\"approve\"}"}]}],"usage":{"input_tokens":100,"output_tokens":20}}`)
	}))
	defer server.Close()
	ctx := WithUsageCollector(t.Context())
	provider := &OpenAIProvider{ModelID: "alias", BaseURL: server.URL, Client: server.Client()}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := provider.Review(ctx, validRequest); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	records := CollectedUsage(ctx)
	if len(records) != 20 {
		t.Fatalf("records=%+v", records)
	}
	for _, record := range records {
		if record.Model != "gpt-5.6-luna-2026-07-09" || *record.InputTokens != 100 || *record.OutputTokens != 20 {
			t.Fatalf("record=%+v", record)
		}
	}
	raw, _ := json.Marshal(records)
	for _, forbidden := range []string{"decision", "approve", "http://", "prompt", "schema"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("leaked %s in %s", forbidden, raw)
		}
	}
	if CollectedUsage(context.Background()) != nil {
		t.Fatal("uninstrumented calls have unknown usage")
	}
	if empty := CollectedUsage(WithUsageCollector(t.Context())); empty == nil || len(empty) != 0 {
		t.Fatal("no calls must produce an explicit empty array")
	}
}

func TestUsageCollectorKeepsIncompleteResponsesAndUnknownUsage(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		known          bool
	}{
		{"incomplete", `{"status":"incomplete","usage":{"input_tokens":100,"output_tokens":20}}`, true},
		{"missing output", `{"usage":{"input_tokens":100,"output_tokens":20}}`, true},
		{"missing usage", `{}`, false},
		{"empty usage", `{"usage":{}}`, false},
		{"partial usage", `{"usage":{"input_tokens":100}}`, false},
		{"invalid response", `oops`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.response) }))
			defer server.Close()
			ctx := WithUsageCollector(t.Context())
			provider := &OpenAIProvider{ModelID: "gpt-5.6-luna", BaseURL: server.URL, Client: server.Client()}
			if _, err := provider.Review(ctx, validRequest); err == nil {
				t.Fatal("expected failure")
			}
			records := CollectedUsage(ctx)
			if len(records) != 1 || (records[0].InputTokens != nil) != tc.known {
				t.Fatalf("records=%+v", records)
			}
			if tc.known && *records[0].InputTokens != 100 {
				t.Fatalf("record=%+v", records[0])
			}
		})
	}
}

func TestUsageCollectorCountsEveryStructuredRetryIncludingExhaustion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"model":"resolved-model","choices":[{"message":{"content":"invalid"}}],"usage":{"prompt_tokens":100,"completion_tokens":20}}`)
	}))
	defer server.Close()
	for _, provider := range []Provider{
		&FireworksProvider{ModelID: "alias", BaseURL: server.URL, Client: server.Client(), StructuredOutputRetries: 1},
		&CamelProvider{ModelID: "auto", BaseURL: server.URL, Client: server.Client(), StructuredOutputRetries: 1},
	} {
		ctx := WithUsageCollector(t.Context())
		if _, err := provider.Review(ctx, validRequest); err == nil {
			t.Fatal("expected exhausted retry error")
		}
		records := CollectedUsage(ctx)
		if len(records) != 2 {
			t.Fatalf("records=%+v", records)
		}
		for _, record := range records {
			if record.Model != "resolved-model" || *record.InputTokens != 100 {
				t.Fatalf("record=%+v", record)
			}
		}
	}
}

func TestUsageCollectorIncludesAnthropicCacheTokensInListInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"model":"claude-sonnet-5","usage":{"input_tokens":100,"cache_read_input_tokens":200,"cache_creation_input_tokens":300,"output_tokens":20}}`)
	}))
	defer server.Close()
	ctx := WithUsageCollector(t.Context())
	provider := &AnthropicProvider{ModelID: "alias", BaseURL: server.URL, Client: server.Client()}
	_, _ = provider.Review(ctx, validRequest)
	records := CollectedUsage(ctx)
	if len(records) != 1 || *records[0].InputTokens != 600 || *records[0].OutputTokens != 20 {
		t.Fatalf("records=%+v", records)
	}
}

func TestUsageCollectorRetainsCodexUsageWhenOutputIsInvalid(t *testing.T) {
	provider := codexTestProvider(t)
	t.Setenv("ADVERSARY_TEST_CODEX_MODE", "invalid")
	ctx := WithUsageCollector(t.Context())
	if _, err := provider.Review(ctx, codexTestRequest()); err == nil {
		t.Fatal("expected schema error")
	}
	records := CollectedUsage(ctx)
	if len(records) != 1 || *records[0].InputTokens != 123 || *records[0].OutputTokens != 45 {
		t.Fatalf("records=%+v", records)
	}
}

func TestUsageCollectorBoundsReportsWithoutClaimingCompleteCosts(t *testing.T) {
	ctx := WithUsageCollector(t.Context())
	for range 4200 {
		observeUsage(ctx, "openai", "model").record("", &Usage{InputTokens: 1})
	}
	records := CollectedUsage(ctx)
	if len(records) != 4096 || records[4095].InputTokens != nil {
		t.Fatalf("unexpected bounded records: %d", len(records))
	}
}
