// Package domain defines the infrastructure-independent review contracts.
package domain

// EvidenceRef is a normalized review contract.
type EvidenceRef struct {
	Kind        string `json:"kind"`
	Path        string `json:"path"`
	ToolCallID  string `json:"tool_call_id"`
	ArtifactID  string `json:"artifact_id"`
	SHA         string `json:"sha"`
	Description string `json:"description"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
}

// Anchor is a normalized review contract.
type Anchor struct {
	Path      string `json:"path"`
	Side      string `json:"side"`
	Commit    string `json:"commit"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// Finding is a normalized review contract.
type Finding struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Scenario    string        `json:"scenario"`
	Consequence string        `json:"consequence"`
	Severity    Severity      `json:"severity"`
	Anchor      Anchor        `json:"anchor"`
	Evidence    []EvidenceRef `json:"evidence"`
	Confidence  float64       `json:"confidence"`
	Assumptions []string      `json:"assumptions"`
	Suggestion  string        `json:"suggestion"`
	ReviewerID  AgentID       `json:"reviewer_id"`
}

// FindingDecision is a normalized review contract.
type FindingDecision struct {
	FindingID  string `json:"finding_id"`
	Decision   string `json:"decision"`
	MergedInto string `json:"merged_into"`
	Reason     string `json:"reason"`
}
