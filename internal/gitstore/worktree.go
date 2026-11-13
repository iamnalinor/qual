package gitstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

// WorktreeRecord tracks retention separately from durable session artifacts.
type WorktreeRecord struct {
	SchemaVersion int            `json:"schema_version"`
	RunID         domain.RunID   `json:"run_id"`
	AgentID       domain.AgentID `json:"agent_id"`
	Path          string         `json:"path"`
	StorePath     string         `json:"store_path"`
	CompletedAt   *time.Time     `json:"completed_at,omitempty"`
	Pinned        bool           `json:"pinned"`
}

// Worktree creates an independent role checkout at the immutable right tree.
func (s *Source) Worktree(ctx context.Context, snap domain.Snapshot, agent domain.AgentID) (result string, err error) {
	defer func() { err = sourceError(err) }()
	if !validID(string(snap.RunID)) || !validID(string(agent)) {
		return "", failure("invalid_config", "unsafe run or agent ID", nil)
	}
	home := filepath.Dir(filepath.Dir(filepath.Dir(snap.StorePath)))
	path := filepath.Join(home, "worktrees", string(snap.RunID), string(agent))
	recordPath := path + ".json"
	if _, e := os.Lstat(path); e == nil {
		record, err := readRecord(recordPath)
		if err != nil || record.StorePath != snap.StorePath {
			return "", failure("source_unavailable", "existing worktree is not registered to snapshot", err)
		}
		return path, nil
	} else if !os.IsNotExist(e) {
		return "", e
	}
	if e := os.MkdirAll(filepath.Dir(path), 0o700); e != nil {
		return "", e
	}
	if _, e := git(ctx, snap.StorePath, nil, nil, "-c", "core.bare=false", "worktree", "add", "--detach", path, snap.HeadCommit); e != nil {
		return "", e
	}
	record := WorktreeRecord{SchemaVersion: 1, RunID: snap.RunID, AgentID: agent, Path: path, StorePath: snap.StorePath}
	if e := writeRecord(recordPath, record); e != nil {
		return "", e
	}
	return path, nil
}

func readRecord(path string) (WorktreeRecord, error) {
	var record WorktreeRecord
	// #nosec G304 -- path is derived from validated Qual-owned registry IDs.
	b, e := os.ReadFile(path)
	if e != nil {
		return record, e
	}
	e = json.Unmarshal(b, &record)
	return record, e
}

func writeRecord(path string, record WorktreeRecord) (err error) {
	b, e := json.Marshal(record)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".registry-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer func() {
		removeErr := os.Remove(name)
		if !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}()
	if _, e = f.Write(b); e != nil {
		return errors.Join(e, f.Close())
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(name, path)
}

// Retain updates completion and pin state; nil completion keeps a worktree active.
func (s *Source) Retain(ctx context.Context, run domain.RunID, agent domain.AgentID, completed *time.Time, pinned bool) (err error) {
	defer func() { err = sourceError(err) }()
	if e := ctx.Err(); e != nil {
		return e
	}
	if !validID(string(run)) || !validID(string(agent)) {
		return failure("invalid_config", "unsafe registry ID", nil)
	}
	path := filepath.Join(s.home, "worktrees", string(run), string(agent)) + ".json"
	record, e := readRecord(path)
	if e != nil {
		return e
	}
	record.CompletedAt = completed
	record.Pinned = pinned
	return writeRecord(path, record)
}

// Cleanup removes completed, unpinned worktrees. Failed removals retain metadata.
// Zero ttl removes all eligible worktrees; positive ttl filters by completion age.
// Negative ttl is invalid.
func (s *Source) Cleanup(ctx context.Context, now time.Time, ttl time.Duration) (result []string, err error) {
	defer func() { err = sourceError(err) }()
	if ttl < 0 {
		return nil, failure("invalid_config", "cleanup TTL must not be negative", nil)
	}
	paths, e := filepath.Glob(filepath.Join(s.home, "worktrees", "*", "*.json"))
	if e != nil {
		return nil, e
	}
	removed := []string{}
	for _, path := range paths {
		if e = ctx.Err(); e != nil {
			return removed, e
		}
		record, err := readRecord(path)
		if err != nil {
			return removed, err
		}
		if record.Pinned || record.CompletedAt == nil || (ttl > 0 && now.Sub(*record.CompletedAt) < ttl) {
			continue
		}
		if err = s.removeRecord(ctx, path, record); err != nil {
			return removed, err
		}
		removed = append(removed, record.Path)
	}
	return removed, nil
}

// Stale compares the selected source again without changing the saved snapshot.
func (s *Source) Stale(ctx context.Context, snap domain.Snapshot) (result bool, err error) {
	defer func() { err = sourceError(err) }()
	b, e := os.ReadFile(filepath.Join(snap.StorePath, "qual-source.json"))
	if errors.Is(e, os.ErrNotExist) {
		head, err := value(ctx, snap.Root, "rev-parse", "--verify", "HEAD^{commit}")
		return head != snap.TargetCommit, err
	}
	if e != nil {
		return false, e
	}
	var meta persistedCapture
	if e = json.Unmarshal(b, &meta); e != nil {
		return false, e
	}
	if snap.Kind == "commits" {
		return false, nil
	}
	head, e := value(ctx, snap.Root, "rev-parse", "--verify", "HEAD^{commit}")
	if e != nil && meta.Head != "" {
		return false, e
	}
	if head != meta.Head {
		return true, nil
	}
	entries, e := capture(ctx, snap.Root, meta.Staged, meta.Untracked)
	if e != nil {
		return false, e
	}
	return fingerprint(entries) != meta.Fingerprint, nil
}

type persistedCapture struct {
	Head        string `json:"head"`
	Staged      bool   `json:"staged"`
	Untracked   bool   `json:"untracked"`
	Fingerprint string `json:"fingerprint"`
}

func fingerprint(entries []entry) string {
	var b []byte
	for _, item := range entries {
		b = append(b, []byte(item.path+"\x00"+item.mode+"\x00"+item.oid+"\x00"+digest(item.data)+"\n")...)
	}
	return digest(b)
}

func (s *Source) removeRecord(ctx context.Context, path string, record WorktreeRecord) error {
	expected := filepath.Join(s.home, "worktrees", string(record.RunID), string(record.AgentID))
	absolute, err := filepath.Abs(expected)
	if err != nil {
		return err
	}
	if record.Path != absolute || !validID(string(record.RunID)) || !validID(string(record.AgentID)) {
		return failure("source_unavailable", "invalid cleanup registry path", nil)
	}
	if _, err = git(ctx, record.StorePath, nil, nil, "worktree", "remove", "--force", record.Path); err != nil {
		return err
	}
	if err = os.Remove(path); err != nil {
		return err
	}
	return nil
}
