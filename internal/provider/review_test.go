package provider

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestNullUsageCountersRemainUnknownAcrossNormalizers(t *testing.T) {
	for _, kind := range []string{"openai", "chatgpt", "codex", "anthropic", "gemini", "openrouter", "openai-compatible"} {
		t.Run(kind, func(t *testing.T) {
			for _, counts := range []struct{ input, output string }{{"null", "null"}, {"null", "0"}, {"0", "null"}, {"0", "0"}} {
				s := newStream(kind, func(domain.ModelEvent) error { return nil })
				payload := usageEvent(kind, counts.input, counts.output)
				if err := s.consume([]byte(payload)); err != nil {
					t.Fatal(err)
				}
				wantKnown := counts.input != "null" && counts.output != "null"
				if s.usage.Known != wantKnown {
					t.Errorf("%s input=%s output=%s known=%v", kind, counts.input, counts.output, s.usage.Known)
				}
			}
		})
	}
}

func usageEvent(kind, input, output string) string {
	switch kind {
	case "openai", "chatgpt", "codex":
		return fmt.Sprintf(`{"type":"response.completed","response":{"usage":{"input_tokens":%s,"output_tokens":%s}}}`, input, output)
	case "anthropic":
		return fmt.Sprintf(`{"type":"message_start","message":{"usage":{"input_tokens":%s,"output_tokens":%s}}}`, input, output)
	case "gemini":
		return fmt.Sprintf(`{"usageMetadata":{"promptTokenCount":%s,"candidatesTokenCount":%s}}`, input, output)
	default:
		return fmt.Sprintf(`{"usage":{"prompt_tokens":%s,"completion_tokens":%s}}`, input, output)
	}
}

func TestAnthropicNullUsageUpdateInvalidatesPriorCounter(t *testing.T) {
	for _, counts := range []struct{ input, output string }{{"null", "1"}, {"1", "null"}} {
		s := newStream("anthropic", func(domain.ModelEvent) error { return nil })
		for _, event := range []string{usageEvent("anthropic", "1", "1"), usageEvent("anthropic", counts.input, counts.output)} {
			if err := s.consume([]byte(event)); err != nil {
				t.Fatal(err)
			}
		}
		if s.usage.Known {
			t.Errorf("null update replaced prior count with known zero: %s/%s", counts.input, counts.output)
		}
	}
}

func TestModelsOverrideIsUniqueAndMatchesRunCapabilities(t *testing.T) {
	override := domain.ModelInfo{ID: "gpt-6-luna", DisplayName: "Verified override", MaxOutputTokens: 7, Tools: false, Streaming: true, Pricing: prices(900000000, 800000000, 700000000, 600000000)}
	duplicate := override
	duplicate.Pricing.InputPerMillion = 1
	p, err := New(Config{Kind: "openai", Capabilities: []domain.ModelInfo{override, duplicate}})
	if err != nil {
		t.Fatal(err)
	}
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("duplicate discovery records: %d", len(models))
	}
	if models[0].Pricing != override.Pricing || models[0].MaxOutputTokens != override.MaxOutputTokens || models[0].Tools || models[0].Provider != "openai" {
		t.Fatalf("wrong override: %#v", models[0])
	}
	request := domain.ModelRequest{Model: "gpt-6-luna", Tools: []domain.ToolDefinition{{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}}
	if err = p.Run(t.Context(), request, func(domain.ModelEvent) error { return nil }); domain.CodeOf(err) != domain.ErrUnsupportedCapability {
		t.Fatalf("Run used a different tools policy: %v", err)
	}
	request.Tools = nil
	request.MaxOutputTokens = 8
	if err = p.Run(t.Context(), request, func(domain.ModelEvent) error { return nil }); domain.CodeOf(err) != domain.ErrUnsupportedCapability {
		t.Fatalf("Run used a different output limit: %v", err)
	}
}
