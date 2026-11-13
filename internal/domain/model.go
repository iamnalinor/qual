// Package domain defines the infrastructure-independent review contracts.
package domain

import (
	"encoding/json"
)

// ToolDefinition is a normalized review contract.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	ReadOnly    bool            `json:"read_only"`
}

// ToolCall is a normalized review contract.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult is a normalized review contract.
type ToolResult struct {
	CallID     string `json:"call_id"`
	Content    string `json:"content"`
	ArtifactID string `json:"artifact_id"`
	IsError    bool   `json:"is_error"`
	Truncated  bool   `json:"truncated"`
}

// Message is a normalized review contract.
type Message struct {
	Role         string          `json:"role"`
	Content      string          `json:"content"`
	ToolCallID   string          `json:"tool_call_id"`
	ToolCalls    []ToolCall      `json:"tool_calls"`
	ProviderData json.RawMessage `json:"provider_data"`
}

// ModelRequest is a normalized review contract.
type ModelRequest struct {
	RunID           RunID            `json:"run_id"`
	AgentID         AgentID          `json:"agent_id"`
	Model           string           `json:"model"`
	ReasoningEffort string           `json:"reasoning_effort"`
	Messages        []Message        `json:"messages"`
	Tools           []ToolDefinition `json:"tools"`
	MaxOutputTokens int              `json:"max_output_tokens"`
	ProviderState   json.RawMessage  `json:"provider_state"`
}

// ModelEvent is a normalized review contract.
type ModelEvent struct {
	Type          string          `json:"type"`
	Text          string          `json:"text"`
	ToolCall      *ToolCall       `json:"tool_call"`
	Usage         *Usage          `json:"usage"`
	ProviderState json.RawMessage `json:"provider_state"`
	Error         *Error          `json:"error"`
}

// ModelInfo is a normalized review contract.
type ModelInfo struct {
	Provider        string           `json:"provider"`
	ID              string           `json:"id"`
	DisplayName     string           `json:"display_name"`
	ContextWindow   int              `json:"context_window"`
	MaxOutputTokens int              `json:"max_output_tokens"`
	ReasoningLevels []string         `json:"reasoning_levels"`
	Tools           bool             `json:"tools"`
	Streaming       bool             `json:"streaming"`
	Pricing         Pricing          `json:"pricing"`
	PricingTiers    []ContextPricing `json:"pricing_tiers,omitempty"`
	UpdatedAt       string           `json:"updated_at"`
	SourceURL       string           `json:"source_url"`
}

// Pricing is a normalized review contract.
type Pricing struct {
	InputPerMillion      Money `json:"input_per_million"`
	OutputPerMillion     Money `json:"output_per_million"`
	CacheReadPerMillion  Money `json:"cache_read_per_million"`
	CacheWritePerMillion Money `json:"cache_write_per_million"`
	Known                bool  `json:"known"`
}

// ContextPricing overrides full-request token prices when input exceeds a threshold.
type ContextPricing struct {
	AboveInputTokens int64   `json:"above_input_tokens"`
	Pricing          Pricing `json:"pricing"`
}
