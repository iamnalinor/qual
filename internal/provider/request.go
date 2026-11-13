package provider

import (
	"encoding/json"
	"net/url"

	"github.com/iamnalinor/qual/internal/domain"
)

func responsesRequest(r domain.ModelRequest, legacy bool) (string, any, error) {
	var input []any
	if len(r.ProviderState) > 0 {
		var state []object
		if err := json.Unmarshal(r.ProviderState, &state); err != nil {
			return "", nil, problem(domain.ErrInvalidConfig, "invalid provider continuation")
		}
		for _, item := range state {
			input = append(input, item)
		}
	}
	for _, m := range r.Messages {
		input = append(input, responseMessage(m)...)
	}
	tools := make([]any, 0, len(r.Tools))
	for _, t := range r.Tools {
		tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.InputSchema})
	}
	body := map[string]any{"model": r.Model, "input": input, "stream": true, "store": false, "tools": tools, "include": []string{"reasoning.encrypted_content"}}
	if r.ReasoningEffort != "" {
		body["reasoning"] = map[string]string{"effort": r.ReasoningEffort}
	}
	if !legacy && r.MaxOutputTokens > 0 {
		body["max_output_tokens"] = r.MaxOutputTokens
	}
	if legacy {
		body["instructions"] = "You are a code reviewer."
	}
	return "/responses", body, nil
}

func responseMessage(m domain.Message) []any {
	if m.Role == "tool" {
		return []any{map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content}}
	}
	var result []any
	if len(m.ProviderData) > 0 {
		var items []object
		if json.Unmarshal(m.ProviderData, &items) == nil {
			for _, item := range items {
				result = append(result, item)
			}
		}
	}
	if m.Content != "" {
		result = append(result, map[string]any{"role": m.Role, "content": m.Content})
	}
	for _, t := range m.ToolCalls {
		result = append(result, map[string]any{"type": "function_call", "call_id": t.ID, "name": t.Name, "arguments": string(t.Arguments)})
	}
	return result
}

func chatRequest(r domain.ModelRequest) (string, any, error) {
	messages := make([]any, 0, len(r.Messages))
	for _, m := range r.Messages {
		message := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.ProviderData) > 0 {
			var details []object
			if json.Unmarshal(m.ProviderData, &details) != nil {
				return "", nil, problem(domain.ErrInvalidConfig, "invalid reasoning continuation")
			}
			message["reasoning_details"] = details
		}
		if m.Role == "tool" {
			message["tool_call_id"] = m.ToolCallID
		}
		if len(m.ToolCalls) > 0 {
			calls := make([]any, 0, len(m.ToolCalls))
			for _, t := range m.ToolCalls {
				calls = append(calls, map[string]any{"id": t.ID, "type": "function", "function": map[string]any{"name": t.Name, "arguments": string(t.Arguments)}})
			}
			message["tool_calls"] = calls
		}
		messages = append(messages, message)
	}
	tools := make([]any, 0, len(r.Tools))
	for _, t := range r.Tools {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.InputSchema}})
	}
	body := map[string]any{"model": r.Model, "messages": messages, "tools": tools, "stream": true, "stream_options": map[string]bool{"include_usage": true}}
	if r.MaxOutputTokens > 0 {
		body["max_tokens"] = r.MaxOutputTokens
	}
	if r.ReasoningEffort != "" {
		body["reasoning_effort"] = r.ReasoningEffort
	}
	return "/chat/completions", body, nil
}

func anthropicRequest(r domain.ModelRequest) (string, any, error) {
	var system []any
	var messages []any
	for _, m := range r.Messages {
		if m.Role == "system" || m.Role == "developer" {
			system = append(system, map[string]string{"type": "text", "text": m.Content})
			continue
		}
		role, content := anthropicMessage(m)
		messages = append(messages, map[string]any{"role": role, "content": content})
	}
	tools := make([]any, 0, len(r.Tools))
	for _, t := range r.Tools {
		tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": t.InputSchema})
	}
	body := map[string]any{"model": r.Model, "messages": messages, "tools": tools, "stream": true, "max_tokens": outputLimit(r.MaxOutputTokens)}
	if len(system) > 0 {
		body["system"] = system
	}
	if r.ReasoningEffort != "" {
		body["thinking"] = map[string]string{"type": "adaptive"}
		body["output_config"] = map[string]string{"effort": r.ReasoningEffort}
	}
	return "/messages", body, nil
}

func anthropicMessage(m domain.Message) (string, []any) {
	if m.Role == "tool" {
		return "user", []any{map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content}}
	}
	var blocks []any
	if len(m.ProviderData) > 0 {
		var prior []object
		if json.Unmarshal(m.ProviderData, &prior) == nil {
			for _, v := range prior {
				blocks = append(blocks, v)
			}
		}
	}
	if m.Content != "" {
		blocks = append(blocks, map[string]string{"type": "text", "text": m.Content})
	}
	for _, t := range m.ToolCalls {
		blocks = append(blocks, map[string]any{"type": "tool_use", "id": t.ID, "name": t.Name, "input": t.Arguments})
	}
	return m.Role, blocks
}

func geminiRequest(r domain.ModelRequest) (string, any, error) {
	var contents []any
	var system []any
	names := make(map[string]string)
	for _, m := range r.Messages {
		for _, call := range m.ToolCalls {
			names[call.ID] = call.Name
		}
	}
	for _, m := range r.Messages {
		if m.Role == "system" || m.Role == "developer" {
			system = append(system, map[string]string{"text": m.Content})
			continue
		}
		role, parts := geminiMessage(m, names)
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	declarations := make([]any, 0, len(r.Tools))
	for _, t := range r.Tools {
		declarations = append(declarations, map[string]any{"name": t.Name, "description": t.Description, "parameters": t.InputSchema})
	}
	body := map[string]any{"contents": contents}
	if len(system) > 0 {
		body["systemInstruction"] = map[string]any{"parts": system}
	}
	if len(declarations) > 0 {
		body["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
	}
	generation := map[string]any{}
	if r.MaxOutputTokens > 0 {
		generation["maxOutputTokens"] = r.MaxOutputTokens
	}
	if r.ReasoningEffort != "" {
		generation["thinkingConfig"] = map[string]string{"thinkingLevel": r.ReasoningEffort}
	}
	body["generationConfig"] = generation
	return "/models/" + url.PathEscape(r.Model) + ":streamGenerateContent?alt=sse", body, nil
}

func geminiMessage(m domain.Message, names map[string]string) (string, []any) {
	if m.Role == "tool" {
		return "user", []any{map[string]any{"functionResponse": map[string]any{"id": m.ToolCallID, "name": names[m.ToolCallID], "response": map[string]string{"result": m.Content}}}}
	}
	role := m.Role
	if role == "assistant" {
		role = "model"
	}
	var parts []any
	if len(m.ProviderData) > 0 {
		var prior []object
		if json.Unmarshal(m.ProviderData, &prior) == nil {
			for _, v := range prior {
				parts = append(parts, v)
			}
		}
	}
	if len(parts) > 0 {
		return role, parts
	}
	if m.Content != "" {
		parts = append(parts, map[string]string{"text": m.Content})
	}
	for _, t := range m.ToolCalls {
		if containsFunction(parts, t.ID) {
			continue
		}
		parts = append(parts, map[string]any{"functionCall": map[string]any{"id": t.ID, "name": t.Name, "args": t.Arguments}})
	}
	return role, parts
}

func containsFunction(parts []any, id string) bool {
	for _, part := range parts {
		if value, ok := part.(object); ok && value.obj("functionCall").str("id") == id {
			return true
		}
	}
	return false
}

func outputLimit(requested int) int {
	if requested > 0 {
		return requested
	}
	return 8192
}
