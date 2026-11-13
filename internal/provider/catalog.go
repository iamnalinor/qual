package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"

	"github.com/iamnalinor/qual/internal/diagnostic"
	"github.com/iamnalinor/qual/internal/domain"
)

// CatalogVersion identifies the pricing and capability snapshot, not entitlement.
const CatalogVersion = "2026-11-13.1"

// Catalog returns conservative verified defaults. Unknown models require explicit
// capability overrides before using tools or reasoning, and have unknown prices.
func Catalog() []domain.ModelInfo {
	luna := domain.ModelInfo{Provider: "openai", ID: "gpt-6-luna", DisplayName: "GPT-6 Luna", ContextWindow: 1050000, MaxOutputTokens: 128000, ReasoningLevels: []string{"none", "low", "medium", "high", "xhigh", "max"}, Tools: true, Streaming: true, Pricing: prices(100000000, 500000000, 10000000, 125000000), PricingTiers: []domain.ContextPricing{{AboveInputTokens: 272000, Pricing: prices(200000000, 750000000, 20000000, 250000000)}}, UpdatedAt: "2026-11-13", SourceURL: "https://developers.openai.com/api/docs/models/gpt-6-luna.md"}
	router := luna
	router.Provider = "openrouter"
	router.ID = "openai/gpt-6-luna"
	router.SourceURL = "https://openrouter.ai/api/v1/models"
	codex := luna
	codex.Provider = "codex"
	codex.Pricing = domain.Pricing{}
	codex.PricingTiers = nil
	codex.SourceURL = "https://github.com/anomalyco/opencode/blob/f6e0c2d0833ffe772035f221fed923e1cd5fb408/packages/core/src/plugin/provider/openai.ts"
	haiku := domain.ModelInfo{Provider: "anthropic", ID: "claude-haiku-5-5", DisplayName: "Claude Haiku 5.5", ContextWindow: 1000000, MaxOutputTokens: 128000, ReasoningLevels: []string{"low", "medium", "high", "xhigh", "max"}, Tools: true, Streaming: true, Pricing: prices(100000000, 500000000, 10000000, 125000000), PricingTiers: []domain.ContextPricing{{AboveInputTokens: 100000, Pricing: prices(500000000, 2500000000, 50000000, 625000000)}}, UpdatedAt: "2026-11-13", SourceURL: "https://platform.claude.com/docs/en/models/haiku-5-5/overview"}
	gemini := domain.ModelInfo{Provider: "gemini", ID: "gemini-3.5-flash-lite", DisplayName: "Gemini 3.5 Flash-Lite", ContextWindow: 1048576, MaxOutputTokens: 65536, ReasoningLevels: []string{"minimal", "low", "medium", "high"}, Tools: true, Streaming: true, Pricing: prices(300000000, 2500000000, 30000000, 0), UpdatedAt: "2026-11-13", SourceURL: "https://aistudio.google.com/docs/models/gemini-3.5-flash-lite"}
	return []domain.ModelInfo{luna, haiku, gemini, router, codex}
}

func prices(input, output, read, write domain.Money) domain.Pricing {
	return domain.Pricing{Known: true, InputPerMillion: input, OutputPerMillion: output, CacheReadPerMillion: read, CacheWritePerMillion: write}
}

func (p *adapter) knownModel(id string) (domain.ModelInfo, bool) {
	for _, model := range p.config.Capabilities {
		if model.ID == id {
			return model, true
		}
	}
	for _, model := range Catalog() {
		if model.ID == id && (model.Provider == p.config.Kind || (p.config.Kind == "chatgpt" && model.Provider == "openai")) {
			return model, true
		}
	}
	return domain.ModelInfo{}, false
}

func (p *adapter) validate(r domain.ModelRequest) error {
	if r.MaxOutputTokens < 0 {
		return problem(domain.ErrInvalidConfig, "output token limit cannot be negative")
	}
	if err := p.protocolSettings(r); err != nil {
		return err
	}
	model, known := p.knownModel(r.Model)
	if !known {
		if len(r.Tools) > 0 || r.ReasoningEffort != "" {
			return problem(domain.ErrUnsupportedCapability, "model capabilities unknown; configure a verified model override")
		}
		return nil
	}
	if len(r.Tools) > 0 && !model.Tools {
		return problem(domain.ErrUnsupportedCapability, "model does not support tools")
	}
	if err := validateReasoning(model, r.ReasoningEffort); err != nil {
		return err
	}
	if model.MaxOutputTokens > 0 && r.MaxOutputTokens > model.MaxOutputTokens {
		return problem(domain.ErrUnsupportedCapability, "requested output exceeds model limit")
	}
	return nil
}

func (p *adapter) Models(ctx context.Context) ([]domain.ModelInfo, error) {
	if p.config.Kind == "chatgpt" {
		return p.accountModels(ctx)
	}
	result := make([]domain.ModelInfo, 0)
	seen := make(map[string]bool)
	for _, model := range Catalog() {
		if model.Provider != p.config.Kind || (p.config.Model != "" && p.config.Model != model.ID) {
			continue
		}
		authoritative, _ := p.knownModel(model.ID)
		authoritative.Provider = p.config.Kind
		result = append(result, authoritative)
		seen[model.ID] = true
	}
	for _, model := range p.config.Capabilities {
		if seen[model.ID] {
			continue
		}
		model.Provider = p.config.Kind
		result = append(result, model)
		seen[model.ID] = true
	}
	if len(result) == 0 && p.config.Model != "" {
		result = append(result, domain.ModelInfo{Provider: p.config.Kind, ID: p.config.Model, DisplayName: p.config.Model})
	}
	return result, nil
}

func (p *adapter) accountModels(ctx context.Context) ([]domain.ModelInfo, error) {
	token, err := p.credential(ctx)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.config.BaseURL+"/models", nil)
	if err != nil {
		return nil, &domain.Error{Code: domain.ErrInvalidConfig, Message: "build model catalog request", Cause: diagnostic.Cause(err, token.AccessToken, token.AccountID)}
	}
	p.headers(request, token)
	response, err := p.client.Do(request)
	if err != nil {
		return nil, transportError(ctx, err, token.AccessToken, token.AccountID)
	}
	defer response.Body.Close() //nolint:errcheck // Catalog decoding already determines the read result.
	if response.StatusCode != http.StatusOK {
		return nil, statusError(response, token.AccessToken, token.AccountID, p.config.APIKey)
	}
	var data struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&data); err != nil {
		return nil, &domain.Error{Code: domain.ErrIncomplete, Message: "invalid account model catalog", Cause: diagnostic.Cause(err, token.AccessToken, token.AccountID)}
	}
	models := make([]domain.ModelInfo, 0, len(data.Models))
	for _, entry := range data.Models {
		if entry.Visibility != "list" {
			continue
		}
		model, _ := p.knownModel(entry.Slug)
		model.Provider = "chatgpt"
		model.ID = entry.Slug
		model.DisplayName = entry.DisplayName
		model.Pricing = domain.Pricing{}
		model.PricingTiers = nil
		models = append(models, model)
	}
	return models, nil
}

func (p *adapter) protocolSettings(r domain.ModelRequest) error {
	if p.config.Kind == "openrouter" && r.Model == "openai/gpt-6-luna" && len(r.Tools) > 0 && r.ReasoningEffort != "none" {
		return problem(domain.ErrUnsupportedCapability, "GPT-6 Luna Chat Completions tool use requires reasoning effort none")
	}
	return nil
}

func validateReasoning(model domain.ModelInfo, effort string) error {
	if effort != "" && !slices.Contains(model.ReasoningLevels, effort) {
		return problem(domain.ErrUnsupportedCapability, "model does not support requested reasoning effort")
	}
	return nil
}
