// Package domain defines the infrastructure-independent review contracts.
package domain

import (
	"encoding/json"
	"time"
)

// Event is a normalized review contract.
type Event struct {
	SchemaVersion int             `json:"schema_version"`
	Sequence      int64           `json:"sequence"`
	RunID         RunID           `json:"run_id"`
	AgentID       AgentID         `json:"agent_id"`
	Type          string          `json:"type"`
	Time          time.Time       `json:"time"`
	Data          json.RawMessage `json:"data"`
}

// Artifact is a normalized review contract.
type Artifact struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// AgentResult is a normalized review contract.
type AgentResult struct {
	AgentID      AgentID           `json:"agent_id"`
	Role         Role              `json:"role"`
	Status       Status            `json:"status"`
	Report       string            `json:"report"`
	Findings     []Finding         `json:"findings"`
	Decisions    []FindingDecision `json:"decisions"`
	Evidence     []EvidenceRef     `json:"evidence"`
	CheckedFiles []string          `json:"checked_files"`
	Questions    []string          `json:"questions"`
	Assumptions  []string          `json:"assumptions"`
	Artifacts    []Artifact        `json:"artifacts"`
	Usage        Usage             `json:"usage"`
}

// RunState is a normalized review contract.
type RunState struct {
	SchemaVersion   int           `json:"schema_version"`
	RunID           RunID         `json:"run_id"`
	Snapshot        Snapshot      `json:"snapshot"`
	Agents          []AgentSpec   `json:"agents"`
	Results         []AgentResult `json:"results"`
	Status          Status        `json:"status"`
	JournalSequence int64         `json:"journal_sequence"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}
