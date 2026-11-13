package provider

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/iamnalinor/qual/internal/domain"
)

type stream struct {
	kind                  string
	emit                  func(domain.ModelEvent) error
	terminal              bool
	stopped               bool
	usage                 domain.Usage
	inputSeen, outputSeen bool
	calls                 map[int]*domain.ToolCall
	opaque                []object
	blocks                map[int]object
	reasoning             map[string]object
	reasoningOrder        []string
}

func newStream(kind string, emit func(domain.ModelEvent) error) *stream {
	return &stream{kind: kind, emit: emit, calls: make(map[int]*domain.ToolCall), blocks: make(map[int]object), reasoning: make(map[string]object)}
}

func (s *stream) consume(data []byte) error {
	if string(data) == "[DONE]" {
		if !s.stopped {
			return problem(domain.ErrIncomplete, "completion marker arrived without finish reason")
		}
		s.terminal = true
		return nil
	}
	event, err := decode(data)
	if err != nil {
		return err
	}
	if event.obj("error") != nil || event.str("type") == "error" {
		return streamError(event.obj("error"))
	}
	switch s.kind {
	case "anthropic":
		return s.anthropic(event)
	case "gemini":
		return s.gemini(event)
	case "openrouter", "openai-compatible":
		return s.chat(event)
	default:
		return s.responses(event)
	}
}

func streamError(e object) *domain.Error {
	code := e.str("code")
	if code == "" {
		code = e.str("type")
	}
	// Provider prose is untrusted and may contain echoed prompt or credentials.
	switch code {
	case "subscription_sharing_usage_limit_exceeded", "subscription_sharing_usage_unavailable":
		return problem(domain.ErrAuth, "ChatGPT plan sharing is unavailable; inspect your account allowance and consent")
	case "rate_limit_exceeded", "rate_limit_error", "overloaded_error", "server_error":
		return &domain.Error{Code: "provider_stream", Message: "provider reported a transient stream failure", Retryable: true}
	default:
		return problem("provider_stream", "provider reported a stream failure")
	}
}

func (s *stream) text(text string) error {
	if text == "" {
		return nil
	}
	return s.emit(domain.ModelEvent{Type: "text", Text: text})
}

func (s *stream) finish() error {
	indices := make([]int, 0, len(s.calls))
	for i := range s.calls {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	for _, i := range indices {
		call := s.calls[i]
		if call.ID == "" || call.Name == "" || !json.Valid(call.Arguments) {
			return problem(domain.ErrIncomplete, "provider returned an incomplete tool call")
		}
		if err := s.emit(domain.ModelEvent{Type: "tool_call", ToolCall: call}); err != nil {
			return err
		}
	}
	s.usage.Billing = "metered"
	if s.kind == "chatgpt" || s.kind == "codex" {
		s.usage.Billing = "subscription"
	}
	if err := s.emit(domain.ModelEvent{Type: "usage", Usage: &s.usage}); err != nil {
		return err
	}
	return s.emit(domain.ModelEvent{Type: "completed", ProviderState: s.continuation()})
}

func (s *stream) responses(e object) error {
	switch e.str("type") {
	case "response.output_text.delta":
		return s.text(e.str("delta"))
	case "response.output_item.done":
		item := e.obj("item")
		index := int(e.num("output_index"))
		switch item.str("type") {
		case "function_call":
			s.calls[index] = &domain.ToolCall{ID: item.str("call_id"), Name: item.str("name"), Arguments: json.RawMessage(item.str("arguments"))}
		case "reasoning":
			s.opaque = append(s.opaque, item)
		}
	case "response.completed":
		response := e.obj("response")
		s.terminal = true
		if response.obj("usage") != nil {
			s.usage = responsesUsage(response.obj("usage"))
		}
	case "response.failed":
		return streamError(e.obj("response").obj("error"))
	case "response.incomplete", "response.cancelled":
		return problem(domain.ErrIncomplete, "provider response did not complete")
	}
	return nil
}

func responsesUsage(u object) domain.Usage {
	return domain.Usage{Known: hasCounters(u, "input_tokens", "output_tokens"), InputTokens: u.num("input_tokens"), OutputTokens: u.num("output_tokens"), CacheReadTokens: u.obj("input_tokens_details").num("cached_tokens"), ReasoningTokens: u.obj("output_tokens_details").num("reasoning_tokens")}
}

func (s *stream) chat(e object) error {
	if u := e.obj("usage"); u != nil {
		s.usage = domain.Usage{Known: hasCounters(u, "prompt_tokens", "completion_tokens"), InputTokens: u.num("prompt_tokens"), OutputTokens: u.num("completion_tokens"), CacheReadTokens: u.obj("prompt_tokens_details").num("cached_tokens"), ReasoningTokens: u.obj("completion_tokens_details").num("reasoning_tokens")}
		s.usage.Cost, s.usage.CostKnown = actualCost(u["cost"])
	}
	for _, choice := range e.list("choices") {
		if err := s.chatChoice(choice); err != nil {
			return err
		}
	}
	return nil
}

func (s *stream) chatChoice(choice object) error {
	delta := choice.obj("delta")
	s.reasoningDelta(delta.list("reasoning_details"))
	if err := s.text(delta.str("content")); err != nil {
		return err
	}
	for _, part := range delta.list("tool_calls") {
		index := int(part.num("index"))
		call := s.calls[index]
		if call == nil {
			call = &domain.ToolCall{}
			s.calls[index] = call
		}
		call.ID += part.str("id")
		function := part.obj("function")
		call.Name += function.str("name")
		call.Arguments = append(call.Arguments, []byte(function.str("arguments"))...)
	}
	reason := choice.str("finish_reason")
	switch reason {
	case "stop", "tool_calls", "function_call":
		s.stopped = true
	case "":
	default:
		return problem(domain.ErrIncomplete, "provider completion stopped before success")
	}
	return nil
}

func (s *stream) anthropic(e object) error {
	index := int(e.num("index"))
	switch e.str("type") {
	case "message_start":
		s.anthropicUsage(e.obj("message").obj("usage"))
	case "content_block_start":
		s.startBlock(index, e.obj("content_block"))
	case "content_block_delta":
		return s.blockDelta(index, e.obj("delta"))
	case "content_block_stop":
		if block := s.blocks[index]; block != nil {
			s.opaque = append(s.opaque, block)
		}
	case "message_delta":
		s.anthropicUsage(e.obj("usage"))
		reason := e.obj("delta").str("stop_reason")
		if reason != "end_turn" && reason != "tool_use" && reason != "stop_sequence" {
			return problem(domain.ErrIncomplete, "Anthropic response stopped before success")
		}
		s.stopped = true
	case "message_stop":
		return s.stopAnthropic()
	}
	return nil
}

func (s *stream) startBlock(index int, block object) {
	switch block.str("type") {
	case "tool_use":
		s.calls[index] = &domain.ToolCall{ID: block.str("id"), Name: block.str("name"), Arguments: block["input"]}
	case "thinking", "redacted_thinking":
		s.blocks[index] = block
	}
}

func (s *stream) blockDelta(index int, delta object) error {
	switch delta.str("type") {
	case "text_delta":
		return s.text(delta.str("text"))
	case "input_json_delta":
		if call := s.calls[index]; call != nil {
			if string(call.Arguments) == "{}" {
				call.Arguments = nil
			}
			call.Arguments = append(call.Arguments, []byte(delta.str("partial_json"))...)
		}
	case "thinking_delta":
		if block := s.blocks[index]; block != nil {
			block["thinking"] = raw(block.str("thinking") + delta.str("thinking"))
		}
	case "signature_delta":
		if block := s.blocks[index]; block != nil {
			block["signature"] = raw(block.str("signature") + delta.str("signature"))
		}
	}
	return nil
}

func (s *stream) anthropicUsage(u object) {
	if u == nil {
		return
	}
	if _, ok := u["input_tokens"]; ok {
		s.inputSeen = u.hasCounter("input_tokens")
		s.usage.CacheReadTokens = u.num("cache_read_input_tokens")
		s.usage.CacheWriteTokens = u.num("cache_creation_input_tokens")
		s.usage.InputTokens = u.num("input_tokens") + s.usage.CacheReadTokens + s.usage.CacheWriteTokens
	}
	if _, ok := u["output_tokens"]; ok {
		s.outputSeen = u.hasCounter("output_tokens")
		s.usage.OutputTokens = u.num("output_tokens")
	}
	s.usage.Known = s.inputSeen && s.outputSeen
}

func (s *stream) gemini(e object) error {
	if e.obj("promptFeedback").str("blockReason") != "" {
		return problem(domain.ErrIncomplete, "Gemini blocked the prompt")
	}
	if u := e.obj("usageMetadata"); u != nil {
		s.usage = domain.Usage{Known: hasCounters(u, "promptTokenCount", "candidatesTokenCount"), InputTokens: u.num("promptTokenCount") + u.num("toolUsePromptTokenCount"), OutputTokens: u.num("candidatesTokenCount") + u.num("thoughtsTokenCount"), ReasoningTokens: u.num("thoughtsTokenCount"), CacheReadTokens: u.num("cachedContentTokenCount")}
	}
	for _, candidate := range e.list("candidates") {
		if err := s.geminiCandidate(candidate); err != nil {
			return err
		}
	}
	return nil
}

func (s *stream) geminiCandidate(candidate object) error {
	for _, part := range candidate.obj("content").list("parts") {
		if err := s.geminiPart(part); err != nil {
			return err
		}
		if call := part.obj("functionCall"); call != nil {
			index := len(s.calls)
			id := call.str("id")
			if id == "" {
				id = fmt.Sprintf("gemini-%d", index)
			}
			s.calls[index] = &domain.ToolCall{ID: id, Name: call.str("name"), Arguments: call["args"]}
		}
	}
	reason := candidate.str("finishReason")
	if reason == "STOP" {
		s.terminal = true
	} else if reason != "" {
		return problem(domain.ErrIncomplete, "Gemini response stopped before success")
	}
	return nil
}

func (s *stream) stopAnthropic() error {
	if !s.stopped {
		return problem(domain.ErrIncomplete, "Anthropic stop reason missing")
	}
	s.terminal = true
	return nil
}

func (s *stream) geminiPart(part object) error {
	s.opaque = append(s.opaque, part)
	var thought bool
	if err := json.Unmarshal(part["thought"], &thought); err != nil {
		thought = false
	}
	if !thought {
		return s.text(part.str("text"))
	}
	return nil
}
