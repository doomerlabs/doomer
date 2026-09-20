package modelreview

import (
	"context"
	"net/http"
)

// CloudflareProvider uses Cloudflare's account-scoped AI REST API. Most models
// use Responses; models documented only for Chat Completions, including Union
// Alpha, use the compatible structured-output path while retaining Cloudflare
// provider provenance.
type CloudflareProvider struct {
	APIKey                    string
	ModelID                   string
	BaseURL                   string
	Headers                   map[string]string
	Client                    *http.Client
	APIMode                   string
	ResponseFormat            string
	StructuredOutputRetries   int
	RequestRetries            int
	MaxOutputTokens           int
	IncludeContentDiagnostics bool
}

func (p *CloudflareProvider) Name() string  { return "cloudflare" }
func (p *CloudflareProvider) Model() string { return p.ModelID }

func (p *CloudflareProvider) Review(ctx context.Context, request Request) (Result, error) {
	if p.MaxOutputTokens > 0 && request.Budget.MaximumOutputTokens > p.MaxOutputTokens {
		request.Budget.MaximumOutputTokens = p.MaxOutputTokens
	}
	if p.APIMode == "chat_completions" {
		return reviewChatCompletionsConfigured(
			ctx, p.Name(), p.APIKey, p.ModelID, p.BaseURL, p.Client, "",
			p.ResponseFormat, p.StructuredOutputRetries, p.RequestRetries,
			p.IncludeContentDiagnostics, request, nil, p.Headers, false,
		)
	}
	return (&OpenAIProvider{
		ProviderName: p.Name(), APIKey: p.APIKey, ModelID: p.ModelID,
		BaseURL: p.BaseURL, Headers: p.Headers, Client: p.Client,
	}).Review(ctx, request)
}
