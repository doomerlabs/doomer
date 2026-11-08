package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

type usage struct {
	CacheCreated           int64       `json:"cache_creation_input_tokens,omitempty"`
	Input                  int64       `json:"input_tokens"`
	Cached                 int64       `json:"cached_input_tokens"`
	Output                 int64       `json:"output_tokens"`
	Cost                   float64     `json:"estimated_cost_usd"`
	Reserved               float64     `json:"budget_charged_usd"`
	Requests               int         `json:"requests"`
	Unknown                int         `json:"requests_without_usage"`
	ProviderHTTPFailures   map[int]int `json:"provider_http_failures,omitempty"`
	BudgetRejections       int         `json:"budget_rejections,omitempty"`
	TokenCountFailures     int         `json:"token_count_failures,omitempty"`
	ContextLimitRejections int         `json:"context_limit_rejections,omitempty"`
}
type meter struct {
	mu         sync.Mutex
	key, model string
	harness    string
	limit      float64
	usage      usage
	client     *http.Client
}

func tokenCost(input, cached, output int64) float64 {
	return (float64(input-cached) + float64(cached)*.05 + float64(output)*5) / 1e6
}
func (m *meter) serve(w http.ResponseWriter, r *http.Request) {
	if m.harness == "claude" {
		m.serveClaude(w, r)
		return
	}
	if r.Method != "POST" || r.URL.Path != "/v1/responses" {
		http.Error(w, "unsupported test endpoint", 404)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1000001))
	if err != nil || len(raw) > 1000000 {
		http.Error(w, "request exceeds test limit", 413)
		return
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body["model"] != m.model {
		http.Error(w, "unexpected model", 400)
		return
	}
	body["max_output_tokens"] = 4096
	body["service_tier"] = "default"
	body["store"] = false
	body["stream"] = true
	raw, _ = json.Marshal(body)
	m.mu.Lock()
	defer m.mu.Unlock()
	inputTokens, err := m.countInput(r.Context(), body)
	if err != nil {
		m.usage.TokenCountFailures++
		http.Error(w, "input token counting failed", 502)
		return
	}
	if inputTokens > 200000 {
		m.usage.ContextLimitRejections++
		http.Error(w, "short-context limit exceeded", 413)
		return
	}
	// Reserve uncached input at the highest documented cache-write rate, plus
	// framing headroom and the enforced output maximum, before inference.
	reserve := (float64(inputTokens+1024)*2.5 + 4096*5) / 1e6
	if m.usage.Reserved+reserve > m.limit {
		m.usage.BudgetRejections++
		http.Error(w, "test API budget exhausted", 429)
		return
	}
	m.usage.Reserved += reserve
	m.usage.Requests++
	req, err := http.NewRequestWithContext(r.Context(), "POST", "https://api.openai.com/v1/responses", bytes.NewReader(raw))
	if err != nil {
		http.Error(w, "request construction failed", 500)
		return
	}
	req.Header.Set("Authorization", "Bearer "+m.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	response, err := m.client.Do(req)
	if err != nil {
		m.usage.Unknown++
		http.Error(w, "upstream request failed", 502)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		m.usage.Unknown++
		if m.usage.ProviderHTTPFailures == nil {
			m.usage.ProviderHTTPFailures = make(map[int]int)
		}
		m.usage.ProviderHTTPFailures[response.StatusCode]++
		// Provider error bodies can contain submitted material. Retain only status.
		http.Error(w, fmt.Sprintf("OpenAI returned HTTP %d", response.StatusCode), response.StatusCode)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 65536), 2000000)
	var recorded bool
	for scanner.Scan() {
		line := scanner.Bytes()
		if bytes.HasPrefix(line, []byte("data: ")) {
			var event struct {
				Type     string `json:"type"`
				Response struct {
					Usage struct {
						Input   int64 `json:"input_tokens"`
						Output  int64 `json:"output_tokens"`
						Details struct {
							Cached int64 `json:"cached_tokens"`
						} `json:"input_tokens_details"`
					} `json:"usage"`
				} `json:"response"`
			}
			if json.Unmarshal(line[6:], &event) == nil && (event.Type == "response.completed" || event.Type == "response.incomplete") {
				u := event.Response.Usage
				if u.Input > 0 && !recorded {
					cost := tokenCost(u.Input, u.Details.Cached, u.Output)
					m.usage.Input += u.Input
					m.usage.Cached += u.Details.Cached
					m.usage.Output += u.Output
					m.usage.Cost += cost
					m.usage.Reserved += cost - reserve
					recorded = true
				}
			}
		}
		if _, err := w.Write(append(append([]byte{}, line...), '\n')); err != nil {
			break
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	if !recorded {
		m.usage.Unknown++
	}
}
func newMeter(c config) *meter {
	return &meter{key: c.APIKey, model: c.Model, harness: c.Harness, limit: c.Budget, client: &http.Client{}}
}

func (m *meter) countInput(ctx context.Context, body map[string]any) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	fields := map[string]any{}
	for _, name := range []string{"model", "conversation", "input", "instructions", "parallel_tool_calls", "personality", "previous_response_id", "reasoning", "text", "tool_choice", "tools", "truncation"} {
		if value, ok := body[name]; ok {
			fields[name] = value
		}
	}
	raw, _ := json.Marshal(fields)
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/responses/input_tokens", bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+m.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("token counter HTTP %d", resp.StatusCode)
	}
	var count struct {
		Input int64 `json:"input_tokens"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&count); err != nil {
		return 0, err
	}
	if count.Input <= 0 {
		return 0, fmt.Errorf("invalid token count")
	}
	return count.Input, nil
}
