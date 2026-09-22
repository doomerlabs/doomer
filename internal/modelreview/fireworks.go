package modelreview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type FireworksProvider struct {
	APIKey                    string
	ModelID                   string
	BaseURL                   string
	Client                    *http.Client
	ReasoningEffort           string
	ResponseFormat            string
	StructuredOutputRetries   int
	RequestRetries            int
	IncludeContentDiagnostics bool
}

// CamelProvider talks to camelStream's OpenAI-compatible Chat Completions
// endpoint using Camel's own credential and configuration namespace.
type CamelProvider struct {
	APIKey                    string
	ModelID                   string
	BaseURL                   string
	Client                    *http.Client
	ReasoningEffort           string
	ResponseFormat            string
	StructuredOutputRetries   int
	RequestRetries            int
	IncludeContentDiagnostics bool
	MaxConcurrency            int
}

type chatCompletionsResponse struct {
	Model   string                  `json:"model"`
	Choices []chatCompletionsChoice `json:"choices"`
	Usage   *struct {
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`
	} `json:"usage"`
}

type chatCompletionsChoice struct {
	Message struct {
		Content          string `json:"content"`
		ReasoningContent string `json:"reasoning_content"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
}

func (p *FireworksProvider) Name() string  { return "fireworks" }
func (p *FireworksProvider) Model() string { return p.ModelID }

func (p *FireworksProvider) Review(ctx context.Context, request Request) (Result, error) {
	return reviewChatCompletions(ctx, p.Name(), p.APIKey, p.ModelID, p.BaseURL, p.Client, p.ReasoningEffort, p.ResponseFormat, p.StructuredOutputRetries, p.RequestRetries, p.IncludeContentDiagnostics, request, nil)
}

func (p *CamelProvider) Name() string  { return "camel" }
func (p *CamelProvider) Model() string { return p.ModelID }

func (p *CamelProvider) Review(ctx context.Context, request Request) (Result, error) {
	gate := sharedCamelGate(p.BaseURL, p.APIKey, p.MaxConcurrency)
	return reviewChatCompletions(ctx, p.Name(), p.APIKey, p.ModelID, p.BaseURL, p.Client, p.ReasoningEffort, p.ResponseFormat, p.StructuredOutputRetries, p.RequestRetries, p.IncludeContentDiagnostics, request, gate)
}

func reviewChatCompletions(ctx context.Context, providerName, apiKey, modelID, baseURL string, client *http.Client, reasoningEffort, responseFormat string, structuredOutputRetries, requestRetries int, includeContentDiagnostics bool, request Request, gate *camelGate) (Result, error) {
	return reviewChatCompletionsConfigured(ctx, providerName, apiKey, modelID, baseURL, client, reasoningEffort, responseFormat, structuredOutputRetries, requestRetries, includeContentDiagnostics, request, gate, nil, true)
}

func reviewChatCompletionsConfigured(ctx context.Context, providerName, apiKey, modelID, baseURL string, client *http.Client, reasoningEffort, responseFormat string, structuredOutputRetries, requestRetries int, includeContentDiagnostics bool, request Request, gate *camelGate, extraHeaders map[string]string, defaultReasoning bool) (Result, error) {
	observation := observeUsage(ctx, providerName, modelID)
	defer observation.finish()
	var schema any
	if err := json.Unmarshal(request.Schema, &schema); err != nil {
		return Result{}, fmt.Errorf("decode model schema: %w", err)
	}
	messages := []map[string]any{
		{"role": "system", "content": request.Prompt},
		{"role": "user", "content": chatCompletionsReviewInput(request)},
	}
	var lastResponse chatCompletionsResponse
	var lastSchemaError error
	usage := Usage{}
	for attempt := 0; attempt <= structuredOutputRetries; attempt++ {
		observation.pending = true
		attemptMessages := messages
		if attempt > 0 {
			attemptMessages = append([]map[string]any{}, messages...)
			if content := firstChatCompletionsContent(lastResponse.Choices); content != "" {
				attemptMessages = append(attemptMessages, map[string]any{
					"role": "assistant", "content": boundedContentPreview(content, 16<<10),
				})
			}
			attemptMessages = append(attemptMessages, map[string]any{
				"role":    "user",
				"content": structuredOutputCorrection(lastResponse.Choices, lastSchemaError),
			})
		}
		payload := map[string]any{
			"model":           modelID,
			"max_tokens":      request.Budget.MaximumOutputTokens,
			"messages":        attemptMessages,
			"response_format": chatCompletionsResponseFormat(responseFormat, schema),
		}
		if reasoningEffort != "" || defaultReasoning {
			payload["reasoning_effort"] = chatCompletionsReasoningEffort(reasoningEffort, request.Budget.MaximumOutputTokens)
		}
		var data []byte
		var status int
		var responseHeaders http.Header
		var err error
		for requestAttempt := 0; ; requestAttempt++ {
			if gate != nil {
				if err := gate.acquire(ctx); err != nil {
					return Result{}, err
				}
			}
			headers := map[string]string{
				"authorization": "Bearer " + apiKey,
			}
			for name, value := range extraHeaders {
				headers[name] = value
			}
			data, status, responseHeaders, err = postJSONWithHeaders(ctx, client, baseURL+"/v1/chat/completions", headers, payload)
			retryable := err != nil || status == http.StatusTooManyRequests || status >= 500
			delay := providerRetryDelay(requestAttempt, responseHeaders)
			if gate != nil {
				delay = camelRetryDelay(requestAttempt, responseHeaders, time.Now())
				// Publish the cooldown before releasing capacity, including on the
				// final retry. Other specialists must not immediately replace a
				// rejected request with another burst.
				if retryable && ctx.Err() == nil {
					gate.cooldown(delay)
				} else if status >= 200 && status < 300 {
					gate.success()
				}
				gate.release()
			}
			if !retryable || requestAttempt >= requestRetries || ctx.Err() != nil {
				break
			}
			if err := waitForProviderRetry(ctx, delay); err != nil {
				return Result{}, err
			}
		}
		if err != nil {
			return Result{}, err
		}
		if status < 200 || status >= 300 {
			failure := providerHTTPError(providerName, status, data)
			if typed, ok := failure.(*ProviderError); ok && typed.Code == "camel_busy" {
				typed.Message = "Camel capacity retry budget exhausted: " + typed.Message
			}
			return Result{}, failure
		}
		var response chatCompletionsResponse
		if err := json.Unmarshal(data, &response); err != nil {
			return Result{}, fmt.Errorf("decode %s response: %w", providerName, err)
		}
		if response.Usage != nil && response.Usage.PromptTokens != nil && response.Usage.CompletionTokens != nil {
			attemptUsage := Usage{InputTokens: *response.Usage.PromptTokens, OutputTokens: *response.Usage.CompletionTokens}
			observation.record(response.Model, &attemptUsage)
			usage.InputTokens += attemptUsage.InputTokens
			usage.OutputTokens += attemptUsage.OutputTokens
		} else {
			observation.record(response.Model, nil)
		}
		lastSchemaError = nil
		for _, choice := range response.Choices {
			for _, output := range compatibleStructuredOutputs(choice.Message.Content) {
				if err := ValidateOutput(request.Schema, output); err == nil {
					return Result{Output: output, Usage: usage}, nil
				} else {
					lastSchemaError = err
				}
			}
		}
		lastResponse = response
	}
	code := providerName + "_missing_output"
	message := chatCompletionsMissingOutputMessage(providerName, lastResponse.Choices, includeContentDiagnostics)
	if lastSchemaError != nil {
		code = providerName + "_invalid_output"
		message = fmt.Sprintf("%s structured output failed schema validation after retries: %v", providerName, lastSchemaError)
		if diagnostics := chatCompletionsChoiceDiagnostics(lastResponse.Choices, includeContentDiagnostics); diagnostics != "" {
			message += " (" + diagnostics + ")"
		}
	}
	return Result{}, &ProviderError{
		Code:    code,
		Message: message,
	}
}

func providerRetryDelay(attempt int, headers http.Header) time.Duration {
	if headers != nil {
		if seconds, err := strconv.Atoi(strings.TrimSpace(headers.Get("Retry-After"))); err == nil && seconds >= 0 {
			delay := time.Duration(seconds) * time.Second
			if delay > 30*time.Second {
				return 30 * time.Second
			}
			return delay
		}
	}
	delay := 250 * time.Millisecond * time.Duration(1<<min(attempt, 6))
	if delay > 10*time.Second {
		return 10 * time.Second
	}
	return delay
}

func waitForProviderRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func chatCompletionsResponseFormat(responseFormat string, schema any) map[string]any {
	if responseFormat == "json_object" {
		return map[string]any{"type": "json_object"}
	}
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "adversary_model_review",
			"schema": schema,
		},
	}
}

func chatCompletionsReasoningEffort(reasoningEffort string, maximumOutputTokens int) string {
	if reasoningEffort != "" {
		return reasoningEffort
	}
	return defaultChatCompletionsReasoningEffort(maximumOutputTokens)
}

func chatCompletionsMissingOutputMessage(providerName string, choices []chatCompletionsChoice, includeContent bool) string {
	diagnostics := chatCompletionsChoiceDiagnostics(choices, includeContent)
	if diagnostics == "" {
		return providerName + " response did not contain structured output (choices=0)"
	}
	return providerName + " response did not contain structured output (" + diagnostics + ")"
}

func chatCompletionsChoiceDiagnostics(choices []chatCompletionsChoice, includeContent bool) string {
	details := make([]string, 0, len(choices))
	for index, choice := range choices {
		detail := fmt.Sprintf(
			"choice[%d]: finish=%q content_bytes=%d reasoning_bytes=%d",
			index,
			choice.FinishReason,
			len(choice.Message.Content),
			len(choice.Message.ReasoningContent),
		)
		if includeContent && choice.Message.Content != "" {
			detail += fmt.Sprintf(" content_preview=%q", boundedContentPreview(choice.Message.Content, 512))
		}
		details = append(details, detail)
	}
	return strings.Join(details, "; ")
}

func (p *FireworksProvider) responseFormat(schema any) map[string]any {
	return chatCompletionsResponseFormat(p.ResponseFormat, schema)
}

func (p *FireworksProvider) reasoningEffort(maximumOutputTokens int) string {
	return chatCompletionsReasoningEffort(p.ReasoningEffort, maximumOutputTokens)
}

func fireworksMissingOutputMessage(choices []chatCompletionsChoice, includeContent bool) string {
	return chatCompletionsMissingOutputMessage("fireworks", choices, includeContent)
}

func boundedContentPreview(content string, maximumBytes int) string {
	if len(content) <= maximumBytes {
		return content
	}
	return strings.ToValidUTF8(content[:maximumBytes], "�") + "…"
}

// compatibleStructuredOutput accepts raw JSON and common wrappers emitted by
// OpenAI-compatible servers that honor the schema semantically but not at the
// transport boundary. The broker still validates the extracted value against
// the requested schema before returning it to the adversary.
func compatibleStructuredOutput(content string) (json.RawMessage, bool) {
	outputs := compatibleStructuredOutputs(content)
	if len(outputs) == 0 {
		return nil, false
	}
	return outputs[0], true
}

func compatibleStructuredOutputs(content string) []json.RawMessage {
	trimmed := strings.TrimSpace(content)
	outputs := make([]json.RawMessage, 0, 2)
	seen := map[string]bool{}
	appendOutput := func(output json.RawMessage) {
		key := string(output)
		if !seen[key] {
			seen[key] = true
			outputs = append(outputs, append(json.RawMessage(nil), output...))
		}
	}
	if json.Valid([]byte(trimmed)) {
		appendOutput(json.RawMessage(trimmed))
	}
	newline := strings.IndexByte(trimmed, '\n')
	if newline >= 0 && strings.HasSuffix(trimmed, "```") {
		opener := strings.TrimSpace(trimmed[:newline])
		if opener == "```" || strings.EqualFold(opener, "```json") {
			body := strings.TrimSpace(strings.TrimSuffix(trimmed[newline+1:], "```"))
			if json.Valid([]byte(body)) {
				appendOutput(json.RawMessage(body))
			}
		}
	}

	for index := range trimmed {
		if trimmed[index] != '{' && trimmed[index] != '[' {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(trimmed[index:]))
		var output json.RawMessage
		if err := decoder.Decode(&output); err == nil && json.Valid(output) {
			appendOutput(output)
		}
	}
	return outputs
}

func firstChatCompletionsContent(choices []chatCompletionsChoice) string {
	for _, choice := range choices {
		if strings.TrimSpace(choice.Message.Content) != "" {
			return choice.Message.Content
		}
	}
	return ""
}

func structuredOutputCorrection(choices []chatCompletionsChoice, schemaError error) string {
	message := "The previous response did not contain valid structured output. Return only one concise JSON value matching the supplied schema, with no prose or markdown."
	if schemaError != nil {
		message += " The previous JSON failed validation: " + boundedContentPreview(schemaError.Error(), 2<<10)
	}
	for _, choice := range choices {
		if choice.FinishReason == "length" {
			message += " The previous response reached the output limit; shorten descriptions and omit all content not required by the schema."
			break
		}
	}
	return message
}

func defaultChatCompletionsReasoningEffort(maximumOutputTokens int) string {
	if maximumOutputTokens <= 2_000 {
		return "none"
	}
	return "low"
}

func fireworksReasoningEffort(maximumOutputTokens int) string {
	return defaultChatCompletionsReasoningEffort(maximumOutputTokens)
}

func chatCompletionsReviewInput(request Request) string {
	return "Review input:\n" + string(request.Input) +
		"\n\nReturn only JSON matching this schema:\n" + string(request.Schema)
}
