// Package port defines boundaries consumed by the review runtime.
package port

import (
	"context"
	"io"

	"github.com/iamnalinor/qual/internal/domain"
)

// Source captures immutable source snapshots and isolates agent worktrees.
type Source interface {
	Prepare(context.Context, domain.SourceRequest) (domain.Snapshot, error)
	Worktree(context.Context, domain.Snapshot, domain.AgentID) (string, error)
	Stale(context.Context, domain.Snapshot) (bool, error)
}

// Provider emits normalized model events and complete tool calls.
type Provider interface {
	Models(context.Context) ([]domain.ModelInfo, error)
	Run(context.Context, domain.ModelRequest, func(domain.ModelEvent) error) error
}

// Tool executes one runtime-owned capability.
type Tool interface {
	Definition() domain.ToolDefinition
	Execute(context.Context, domain.ToolCall) (domain.ToolResult, error)
}

// Store persists session state, ordered events, and immutable artifacts.
type Store interface {
	Create(context.Context, domain.RunState) error
	Load(context.Context, domain.RunID) (domain.RunState, error)
	Save(context.Context, domain.RunState) error
	Append(context.Context, domain.Event) error
	Events(context.Context, domain.RunID) ([]domain.Event, error)
	PutArtifact(context.Context, domain.RunID, string, io.Reader) (domain.Artifact, error)
	List(context.Context) ([]domain.RunState, error)
}
