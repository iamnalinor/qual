// Package domain defines the infrastructure-independent review contracts.
package domain

// AgentSpec is a normalized review contract.
type AgentSpec struct {
	ID                  AgentID      `json:"id"`
	Role                Role         `json:"role"`
	Provider            string       `json:"provider"`
	Model               string       `json:"model"`
	ReasoningEffort     string       `json:"reasoning_effort"`
	InvestigationEffort string       `json:"investigation_effort"`
	Focus               string       `json:"focus"`
	Instructions        string       `json:"instructions"`
	Language            string       `json:"language"`
	Budget              BudgetLimits `json:"budget"`
}

// BudgetLimits is a normalized review contract.
type BudgetLimits struct {
	Soft Money `json:"soft"`
	Hard Money `json:"hard"`
}

// Usage is a normalized review contract.
type Usage struct {
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	ReasoningTokens  int64  `json:"reasoning_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
	Known            bool   `json:"known"`
	Cost             Money  `json:"cost"`
	CostKnown        bool   `json:"cost_known"`
	Billing          string `json:"billing"`
}
