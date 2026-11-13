// Package domain defines the infrastructure-independent review contracts.
package domain

import (
	"time"
)

// SourceRequest is a normalized review contract.
type SourceRequest struct {
	Target           string `json:"target"`
	Base             string `json:"base"`
	Head             string `json:"head"`
	Staged           bool   `json:"staged"`
	Worktree         bool   `json:"worktree"`
	IncludeUntracked bool   `json:"include_untracked"`
	AllowEmpty       bool   `json:"allow_empty"`
	RunID            RunID  `json:"run_id"`
	Home             string `json:"home"`
}

// Snapshot is a normalized review contract.
type Snapshot struct {
	SchemaVersion int           `json:"schema_version"`
	RunID         RunID         `json:"run_id"`
	ProjectID     ProjectID     `json:"project_id"`
	Kind          string        `json:"kind"`
	Repository    string        `json:"repository"`
	Root          string        `json:"root"`
	StorePath     string        `json:"store_path"`
	BaseCommit    string        `json:"base_commit"`
	HeadCommit    string        `json:"head_commit"`
	OriginalHead  string        `json:"original_head"`
	TargetCommit  string        `json:"target_commit"`
	BaseTree      string        `json:"base_tree"`
	HeadTree      string        `json:"head_tree"`
	DiffHash      string        `json:"diff_hash"`
	RulesRevision string        `json:"rules_revision"`
	Files         []ChangedFile `json:"files"`
	CapturedAt    time.Time     `json:"captured_at"`
}

// ChangedFile is a normalized review contract.
type ChangedFile struct {
	Path           string `json:"path"`
	OldPath        string `json:"old_path"`
	Status         string `json:"status"`
	ExcludedReason string `json:"excluded_reason"`
	Binary         bool   `json:"binary"`
}
