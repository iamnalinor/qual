package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/gitstore"
)

// Config selects trusted hosts, host-specific credentials and an optional CI checkout.
// APIBaseURLs is trusted application configuration, never repository configuration.
type Config struct {
	// GitCAFile optionally selects a trusted CA bundle for self-managed HTTPS Git.
	GitCAFile   string
	Home        string
	GitLabHosts []string
	Tokens      map[string]string
	HTTPClient  *http.Client
	APIBaseURLs map[string]string
	Checkout    string
}

// Source composes local Git and hosted review sources.
type Source struct {
	config Config
	local  *gitstore.Source
}

// New constructs a source without reading credentials from the environment.
func New(config Config) *Source { return &Source{config: config, local: gitstore.New(config.Home)} }

// ReviewContext preserves platform context and exact source provenance.
type ReviewContext struct {
	SchemaVersion  int                          `json:"schema_version"`
	Target         Target                       `json:"target"`
	Title          string                       `json:"title"`
	Body           string                       `json:"body"`
	Head           string                       `json:"head"`
	TargetSHA      string                       `json:"target_sha"`
	HeadRepository string                       `json:"head_repository"`
	HeadRef        string                       `json:"head_ref"`
	TargetRef      string                       `json:"target_ref"`
	HeadURL        string                       `json:"head_url"`
	TargetURL      string                       `json:"target_url"`
	Items          map[string][]json.RawMessage `json:"items"`
}

// Prepare fixes H/T/B and imports their objects into independent storage.
func (s *Source) Prepare(ctx context.Context, req domain.SourceRequest) (result domain.Snapshot, resultErr error) {
	defer func() { resultErr = sourceError(resultErr) }()
	if !strings.Contains(req.Target, "://") {
		return s.local.Prepare(ctx, req)
	}
	var empty domain.Snapshot
	if req.Base != "" || req.Head != "" || req.Staged || req.Worktree || req.IncludeUntracked {
		return empty, failure("invalid_config", "local selectors cannot be used with a review URL", nil)
	}
	target, err := ParseTarget(req.Target, s.config.GitLabHosts)
	if err != nil {
		return empty, err
	}
	metadata, err := s.metadata(ctx, target, true)
	if err != nil {
		return empty, err
	}
	repo, cleanup, err := s.repository(ctx, metadata)
	if err != nil {
		return empty, err
	}
	defer func() { resultErr = errors.Join(resultErr, cleanup()) }()
	base, err := s.mergeBase(ctx, repo, metadata)
	if err != nil {
		return empty, err
	}
	return s.importSnapshot(ctx, req, repo, base, metadata)
}

func (s *Source) importSnapshot(ctx context.Context, req domain.SourceRequest, repo, base string, metadata ReviewContext) (domain.Snapshot, error) {
	var empty domain.Snapshot
	target := metadata.Target
	snap, err := s.local.Import(ctx, req, repo, base, metadata.Head)
	if err != nil {
		return empty, err
	}
	if err = s.local.ImportRevision(ctx, snap, repo, metadata.TargetSHA); err != nil {
		return empty, err
	}
	snap.Kind = target.Kind
	snap.Root = ""
	snap.Repository = "https://" + target.Host + "/" + target.Repository
	identity := sha256.Sum256([]byte(snap.Repository))
	snap.ProjectID = domain.ProjectID(hex.EncodeToString(identity[:]))
	snap.TargetCommit = metadata.TargetSHA
	snap.RulesRevision = metadata.TargetSHA
	data, err := json.Marshal(metadata)
	if err != nil {
		return empty, err
	}
	if err = os.WriteFile(filepath.Join(snap.StorePath, "qual-platform.json"), data, 0o600); err != nil {
		return empty, failure("source_unavailable", "save platform context", err)
	}
	return snap, nil
}

// Context loads the saved platform metadata without contacting the platform.
func (s *Source) Context(snap domain.Snapshot) (ReviewContext, error) {
	var metadata ReviewContext
	data, err := os.ReadFile(filepath.Join(snap.StorePath, "qual-platform.json"))
	if err != nil {
		return metadata, failure("source_unavailable", "read platform context", err)
	}
	if err = json.Unmarshal(data, &metadata); err != nil {
		return metadata, failure("source_unavailable", "decode platform context", err)
	}
	return metadata, nil
}

// Worktree delegates immutable tree materialization to gitstore.
func (s *Source) Worktree(ctx context.Context, snap domain.Snapshot, agent domain.AgentID) (string, error) {
	return s.local.Worktree(ctx, snap, agent)
}

// Stale compares current hosted head with the saved source head.
func (s *Source) Stale(ctx context.Context, snap domain.Snapshot) (bool, error) {
	if snap.Kind != "github" && snap.Kind != "gitlab" {
		return s.local.Stale(ctx, snap)
	}
	saved, err := s.Context(snap)
	if err != nil {
		return false, err
	}
	current, err := s.metadata(ctx, saved.Target, false)
	if err != nil {
		return false, err
	}
	return current.Head != saved.Head, nil
}

func sourceError(err error) error {
	if err != nil && domain.CodeOf(err) == "" {
		return failure("source_unavailable", "prepare platform snapshot", err)
	}
	return err
}
