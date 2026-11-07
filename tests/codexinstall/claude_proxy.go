package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

func claudeTokenCost(input, cached, created, output int64) float64 {
	return (float64(input-cached-created)*.10 + float64(cached)*.01 + float64(created)*.125 + float64(output)*.50) / 1e6
}
func (m *meter) claudeRequest(ctx context.Context, path string, body any, original *http.Request) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", m.key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	if beta := original.Header.Get("anthropic-beta"); beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
	return m.client.Do(req)
}
func (m *meter) serveClaude(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.Path != "/v1/messages" {
		http.Error(w, "unsupported test endpoint", 404)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4000001))
	if err != nil || len(raw) > 4000000 {
		http.Error(w, "request exceeds test limit", 413)
		return
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body["model"] != m.model {
		http.Error(w, "unexpected model", 400)
		return
	}
	body["max_tokens"] = 4096
	body["stream"] = true
	body["service_tier"] = "standard_only"
	delete(body, "speed")
	delete(body, "inference_geo")
	countBody := map[string]any{}
	for _, field := range []string{"model", "messages", "system", "tools", "tool_choice", "thinking"} {
		if value, ok := body[field]; ok {
			countBody[field] = value
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	countResponse, err := m.claudeRequest(ctx, "/v1/messages/count_tokens", countBody, r)
	if err != nil {
		cancel()
		http.Error(w, "input token counting failed", 502)
		return
	}
	var count struct {
		Input int64 `json:"input_tokens"`
	}
	countErr := json.NewDecoder(io.LimitReader(countResponse.Body, 65536)).Decode(&count)
	countResponse.Body.Close()
	cancel()
	if countResponse.StatusCode != 200 || countErr != nil || count.Input <= 0 || count.Input > 98000 {
		http.Error(w, "input token counting failed or short-context limit exceeded", 502)
		return
	}
	reserve := (float64(count.Input+1024)*.20 + 4096*.5) / 1e6
	m.mu.Lock()
	if m.usage.Reserved+reserve > m.limit {
		m.mu.Unlock()
		http.Error(w, "test API budget exhausted", 429)
		return
	}
	m.usage.Reserved += reserve
	m.usage.Requests++
	m.mu.Unlock()
	relayCtx, relayCancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer relayCancel()
	response, err := m.claudeRequest(relayCtx, "/v1/messages", body, r)
	if err != nil {
		m.mu.Lock()
		m.usage.Unknown++
		m.mu.Unlock()
		http.Error(w, "upstream request failed", 502)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		m.mu.Lock()
		m.usage.Unknown++
		m.mu.Unlock()
		http.Error(w, fmt.Sprintf("Anthropic returned HTTP %d", response.StatusCode), response.StatusCode)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 65536), 2000000)
	var observed struct {
		Input    int64 `json:"input_tokens"`
		Output   int64 `json:"output_tokens"`
		Cached   int64 `json:"cache_read_input_tokens"`
		Created  int64 `json:"cache_creation_input_tokens"`
		Creation struct {
			Hour int64 `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
	}
	recorded := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if bytes.HasPrefix(line, []byte("data: ")) {
			var event struct {
				Type    string          `json:"type"`
				Usage   json.RawMessage `json:"usage"`
				Message struct {
					Usage json.RawMessage `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(line[6:], &event) == nil {
				if len(event.Message.Usage) > 0 {
					_ = json.Unmarshal(event.Message.Usage, &observed)
				}
				if len(event.Usage) > 0 {
					_ = json.Unmarshal(event.Usage, &observed)
				}
				if event.Type == "message_stop" && !recorded && observed.Input+observed.Cached+observed.Created > 0 {
					input := observed.Input + observed.Cached + observed.Created
					cost := claudeTokenCost(input, observed.Cached, observed.Created, observed.Output) + float64(observed.Creation.Hour)*.075/1e6
					m.mu.Lock()
					m.usage.Input += input
					m.usage.Cached += observed.Cached
					m.usage.CacheCreated += observed.Created
					m.usage.Output += observed.Output
					m.usage.Cost += cost
					m.usage.Reserved += cost - reserve
					m.mu.Unlock()
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
		m.mu.Lock()
		m.usage.Unknown++
		m.mu.Unlock()
	}
}
