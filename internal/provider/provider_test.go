package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

//nolint:gocognit,gocyclo // The table runs complete HTTP/SSE protocol scenarios with per-event assertions.
func TestProtocolRoutesAndCompleteTools(t *testing.T) {
	cases := []struct{ kind, path, stream string }{
		{"openai", "/responses", `data: {"type":"response.output_text.delta","delta":"hello"}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"c","name":"read","arguments":"{\"path\":\"x\"}"}}

data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}}

`},
		{"chatgpt", "/responses", `data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5}}}

`},
		{"codex", "/responses", `data: {"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5}}}

`},
		{"anthropic", "/messages", `data: {"type":"message_start","message":{"usage":{"input_tokens":5,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c","name":"read","input":{}}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"x\"}"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

data: {"type":"message_stop"}

`},
		{"gemini", "/models/test:streamGenerateContent", `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":3,"thoughtsTokenCount":2}}

`},
		{"openrouter", "/chat/completions", chatFixture},
		{"openai-compatible", "/chat/completions", chatFixture},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			var attempts int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				if r.URL.Path != tc.path {
					t.Errorf("path %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if tc.kind == "chatgpt" && (body["store"] != false || body["stream"] != true) {
					t.Errorf("subscription flags %v", body)
				}
				if tc.kind == "codex" && body["max_output_tokens"] != nil {
					t.Error("legacy max_output_tokens")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if _, err := fmt.Fprint(w, tc.stream); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			p, err := New(Config{Kind: tc.kind, BaseURL: server.URL, APIKey: "fixture", Model: "test", HTTPClient: server.Client(), TokenSource: TokenSourceFunc(func(context.Context) (Credential, error) {
				return Credential{AccessToken: "oauth", AccountID: "account"}, nil
			}), Capabilities: []domain.ModelInfo{{ID: "test", Tools: true, Streaming: true}}})
			if err != nil {
				t.Fatal(err)
			}
			var events []domain.ModelEvent
			err = p.Run(t.Context(), domain.ModelRequest{Model: "test", MaxOutputTokens: 20, Messages: []domain.Message{{Role: "user", Content: "read"}}}, func(e domain.ModelEvent) error { events = append(events, e); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if attempts != 1 || events[len(events)-1].Type != "completed" {
				t.Fatalf("attempts/events %d %v", attempts, events)
			}
			toolCount := 0
			for _, event := range events {
				if event.ToolCall != nil {
					toolCount++
				}
			}
			if tc.kind != "chatgpt" && tc.kind != "codex" && toolCount != 1 {
				t.Fatalf("complete tool events %d", toolCount)
			}
			for _, e := range events {
				if e.ToolCall != nil && (!json.Valid(e.ToolCall.Arguments) || !strings.Contains(string(e.ToolCall.Arguments), "x")) {
					t.Errorf("tool %#v", e.ToolCall)
				}
			}
			var usage *domain.Usage
			for _, e := range events {
				if e.Usage != nil {
					usage = e.Usage
				}
			}
			if usage == nil || !usage.Known || usage.InputTokens != 10 || usage.OutputTokens != 5 {
				t.Errorf("usage %#v", usage)
			}
		})
	}
}

const chatFixture = `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"read","arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}

data: [DONE]

`

func TestFailureNeverRetriesOrLeaksBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(429)
		if _, err := fmt.Fprint(w, `{"error":{"message":"secret-token"}}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	p, err := New(Config{Kind: "openai", BaseURL: server.URL, APIKey: "secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	err = p.Run(t.Context(), domain.ModelRequest{Model: "test"}, func(domain.ModelEvent) error { return nil })
	var coded *domain.Error
	if !errors.As(err, &coded) || !coded.Retryable || coded.RetryAfter != 2*time.Second || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error %v", err)
	}
}

func TestIncompleteAndCallbackStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n"); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	p, err := New(Config{Kind: "openai", BaseURL: server.URL, APIKey: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Run(t.Context(), domain.ModelRequest{Model: "test"}, func(domain.ModelEvent) error { return nil }); domain.CodeOf(err) != domain.ErrIncomplete {
		t.Fatalf("EOF %v", err)
	}
	sentinel := errors.New("callback stopped")
	if err = p.Run(t.Context(), domain.ModelRequest{Model: "test"}, func(domain.ModelEvent) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("callback %v", err)
	}
}

func TestUnknownCapabilitiesAndSubscriptionNeverUseAPIKey(t *testing.T) {
	p, err := New(Config{Kind: "chatgpt", APIKey: "must-not-be-used"})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Run(t.Context(), domain.ModelRequest{Model: "unknown"}, func(domain.ModelEvent) error { return nil }); domain.CodeOf(err) != domain.ErrAuth {
		t.Fatalf("fallback: %v", err)
	}
	p, err = New(Config{Kind: "openai-compatible", BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Run(t.Context(), domain.ModelRequest{Model: "unknown", Tools: []domain.ToolDefinition{{Name: "read"}}}, func(domain.ModelEvent) error { return nil }); domain.CodeOf(err) != domain.ErrUnsupportedCapability {
		t.Fatalf("capabilities: %v", err)
	}
}

func TestGeminiToolResultsReplayOriginalNameAndSignatureOnce(t *testing.T) {
	request := domain.ModelRequest{Model: "test", Messages: []domain.Message{{Role: "assistant", ToolCalls: []domain.ToolCall{{ID: "call-0", Name: "read_file", Arguments: json.RawMessage(`{"path":"x"}`)}}, ProviderData: json.RawMessage(`[{"functionCall":{"id":"call-0","name":"read_file","args":{"path":"x"}},"thoughtSignature":"opaque"}]`)}, {Role: "tool", ToolCallID: "call-0", Content: "file"}}}
	_, body, err := geminiRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `"functionCall"`) != 1 || !strings.Contains(string(data), `"functionResponse":{"id":"call-0","name":"read_file"`) {
		t.Fatalf("replay %s", data)
	}
}

func TestUsageAbsentCountersRemainUnknownAndActualCostIsExact(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic", "gemini", "openrouter"} {
		t.Run(kind, func(t *testing.T) {
			s := newStream(kind, func(domain.ModelEvent) error { return nil })
			payload := map[string]string{"openai": `{"type":"response.completed","response":{"usage":{}}}`, "anthropic": `{"type":"message_start","message":{"usage":{}}}`, "gemini": `{"usageMetadata":{}}`, "openrouter": `{"usage":{}}`}
			if err := s.consume([]byte(payload[kind])); err != nil {
				t.Fatal(err)
			}
			if s.usage.Known {
				t.Error("missing usage counters reported as zero")
			}
		})
	}
	s := newStream("openrouter", func(domain.ModelEvent) error { return nil })
	if err := s.consume([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.000001234}}`)); err != nil {
		t.Fatal(err)
	}
	if !s.usage.CostKnown || s.usage.Cost != 1234 {
		t.Fatalf("cost %#v", s.usage)
	}
}

func TestChatGPTModelsUseSelectedAccountAndVisibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer account-token" {
			t.Errorf("catalog request %s", r.URL.Path)
		}
		if _, err := fmt.Fprint(w, `{"models":[{"slug":"second","display_name":"Second","visibility":"list"},{"slug":"hidden","visibility":"hidden"},{"slug":"first","display_name":"First","visibility":"list"}]}`); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	p, err := New(Config{Kind: "chatgpt", BaseURL: server.URL, TokenSource: TokenSourceFunc(func(context.Context) (Credential, error) { return Credential{AccessToken: "account-token"}, nil })})
	if err != nil {
		t.Fatal(err)
	}
	models, err := p.Models(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "second" || models[1].ID != "first" || models[0].Pricing.Known {
		t.Fatalf("catalog %#v", models)
	}
}

func TestCancellationInterruptsStream(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	p, err := New(Config{Kind: "openai", BaseURL: server.URL, APIKey: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- p.Run(ctx, domain.ModelRequest{Model: "test"}, func(domain.ModelEvent) error { return nil })
	}()
	<-started
	cancel()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream failed to cancel")
	}
}

func TestGeminiSignedTextContinuationDoesNotDuplicateVisibleContent(t *testing.T) {
	s := newStream("gemini", func(domain.ModelEvent) error { return nil })
	if err := s.consume([]byte(`{"candidates":[{"content":{"parts":[{"text":"answer","thoughtSignature":"opaque"}]},"finishReason":"STOP"}]}`)); err != nil {
		t.Fatal(err)
	}
	_, parts := geminiMessage(domain.Message{Role: "assistant", Content: "answer", ProviderData: raw(s.opaque)}, nil)
	data, err := json.Marshal(parts)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `"text":"answer"`) != 1 {
		t.Fatalf("repeated signed content %s", data)
	}
}

//nolint:gocyclo // The cross-route catalog check asserts capabilities, provenance and tier semantics together.
func TestCatalogSupportsHostedRoutesAndContextPriceTiers(t *testing.T) {
	for _, kind := range []string{"openai", "anthropic", "gemini", "openrouter", "codex"} {
		t.Run(kind, func(t *testing.T) {
			p, err := New(Config{Kind: kind})
			if err != nil {
				t.Fatal(err)
			}
			models, err := p.Models(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(models) == 0 || !models[0].Tools || models[0].SourceURL == "" || models[0].UpdatedAt == "" {
				t.Fatalf("missing verified model for %s", kind)
			}
			if kind == "openai" && (models[0].ContextWindow != 1050000 || models[0].MaxOutputTokens != 128000 || len(models[0].PricingTiers) != 1 || models[0].PricingTiers[0].AboveInputTokens != 272000) {
				t.Fatalf("incorrect real limits/tiers %#v", models[0])
			}
		})
	}
}

func TestEveryRouteHTTPErrorAndUnsupportedSettings(t *testing.T) {
	for _, kind := range []string{"openai", "chatgpt", "codex", "anthropic", "gemini", "openrouter", "openai-compatible"} {
		t.Run(kind, func(t *testing.T) {
			var count atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				count.Add(1)
				w.Header().Set("Retry-After", "2")
				w.WriteHeader(http.StatusTooManyRequests)
				if _, err := fmt.Fprint(w, `{"error":{"message":"private-fixture"}}`); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			p, err := New(Config{Kind: kind, BaseURL: server.URL, APIKey: "private-fixture", TokenSource: TokenSourceFunc(func(context.Context) (Credential, error) {
				return Credential{AccessToken: "private-fixture", Kind: kind}, nil
			}), Capabilities: []domain.ModelInfo{{ID: "fixture", Streaming: true}}})
			if err != nil {
				t.Fatal(err)
			}
			request := domain.ModelRequest{Model: "fixture", Tools: []domain.ToolDefinition{{Name: "read"}}}
			if err = p.Run(t.Context(), request, func(domain.ModelEvent) error { return nil }); domain.CodeOf(err) != domain.ErrUnsupportedCapability {
				t.Fatalf("unsupported tool: %v", err)
			}
			request.Tools = nil
			err = p.Run(t.Context(), request, func(domain.ModelEvent) error { return nil })
			var coded *domain.Error
			if !errors.As(err, &coded) || coded.HTTPStatus != 429 || !coded.Retryable || coded.RetryAfter != 2*time.Second || count.Load() != 1 || strings.Contains(err.Error(), "private-fixture") {
				t.Fatalf("HTTP policy %v, calls=%d", err, count.Load())
			}
		})
	}
}

func TestSubscriptionRejectsCredentialFromOtherRoute(t *testing.T) {
	p, err := New(Config{Kind: "chatgpt", APIKey: "must-not-use", TokenSource: TokenSourceFunc(func(context.Context) (Credential, error) {
		return Credential{Kind: "codex", AccessToken: "legacy"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Run(t.Context(), domain.ModelRequest{Model: "gpt-6-luna"}, func(domain.ModelEvent) error { return nil }); domain.CodeOf(err) != domain.ErrAuth {
		t.Fatalf("cross-route credential accepted: %v", err)
	}
}

func TestOpenRouterReasoningContinuityRemainsOpaque(t *testing.T) {
	var state json.RawMessage
	var visible strings.Builder
	s := newStream("openrouter", func(e domain.ModelEvent) error {
		if e.Type == "completed" {
			state = e.ProviderState
		}
		_, err := visible.WriteString(e.Text)
		return err
	})
	for _, payload := range []string{`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","index":0,"id":"r","data":"opaque-"}]}}]}`, `{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","index":0,"data":"continued"}]},"finish_reason":"stop"}]}`, `[DONE]`} {
		if err := s.consume([]byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.finish(); err != nil {
		t.Fatal(err)
	}
	if visible.Len() != 0 || !strings.Contains(string(state), "opaque-continued") {
		t.Fatalf("continuation absent or visible: %s", state)
	}
	_, body, err := chatRequest(domain.ModelRequest{Model: "fixture", Messages: []domain.Message{{Role: "assistant", Content: "visible", ProviderData: state}}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"reasoning_details"`) {
		t.Fatal("continuation was not replayed")
	}
}
