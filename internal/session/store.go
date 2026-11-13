// Package session persists private checkpoints, append-only journals and immutable artifacts.
package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/iamnalinor/qual/internal/domain"
)

// Store stores runs below QUAL_HOME/sessions.
type Store struct {
	home string
	mu   sync.Mutex
}

// New constructs a filesystem store without creating files.
func New(home string) *Store { return &Store{home: home} }

func (s *Store) run(id domain.RunID) (string, error) {
	if id == "" || strings.ContainsAny(string(id), "/\\") || id == "." || id == ".." {
		return "", fmt.Errorf("invalid run ID")
	}
	return filepath.Join(s.home, "sessions", string(id)), nil
}

// Create exclusively creates a new session.
func (s *Store) Create(ctx context.Context, state domain.RunState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.run(state.RunID)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fmt.Errorf("create sessions: %w", err)
	}
	if err = os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	return s.Save(ctx, state)
}

// Load reads the latest atomic checkpoint.
func (s *Store) Load(ctx context.Context, id domain.RunID) (domain.RunState, error) {
	var state domain.RunState
	if err := ctx.Err(); err != nil {
		return state, err
	}
	dir, err := s.run(id)
	if err != nil {
		return state, err
	}
	// Run IDs are validated by run; checkpoint name is a constant.
	data, err := os.ReadFile(filepath.Join(dir, "checkpoint.json")) //nolint:gosec // Confined session checkpoint path.
	if err != nil {
		return state, fmt.Errorf("read checkpoint: %w", err)
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode checkpoint: %w", err)
	}
	return state, nil
}

// Save fsyncs and atomically replaces the checkpoint.
func (s *Store) Save(ctx context.Context, state domain.RunState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.run(state.RunID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	return atomicWrite(dir, "checkpoint.json", data)
}

func atomicWrite(dir, name string, data []byte) (err error) {
	f, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	path := f.Name()
	defer func() {
		removeErr := os.Remove(path)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write temporary file: %w", err), f.Close())
	}
	if err = f.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync temporary file: %w", err), f.Close())
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}
	if err = os.Rename(path, filepath.Join(dir, name)); err != nil {
		return fmt.Errorf("replace file: %w", err)
	}
	d, err := os.Open(dir) //nolint:gosec // Directory is the already validated session or artifact directory.
	if err != nil {
		return fmt.Errorf("open directory: %w", err)
	}
	return errors.Join(d.Sync(), d.Close())
}

// Lock obtains one process owner; kernel locks release automatically after a crash.
func (s *Store) Lock(ctx context.Context, id domain.RunID) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := s.run(id)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "owner.lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // Validated run ID and fixed owner-lock name.
	if err != nil {
		return nil, fmt.Errorf("open run lock: %w", err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(fmt.Errorf("run already owned: %w", err), f.Close())
	}
	return func() error { return errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close()) }, nil
}

// Append commits a single complete JSONL event before returning.
func (s *Store) Append(ctx context.Context, event domain.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.run(event.RunID)
	if err != nil {
		return err
	}
	events, err := s.Events(ctx, event.RunID)
	if err != nil {
		return err
	}
	event.Sequence = int64(len(events)) + 1
	event.SchemaVersion = 1
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	// Replace a torn tail before appending another event.
	var valid int64
	for _, e := range events {
		line, eerr := json.Marshal(e)
		if eerr != nil {
			return fmt.Errorf("encode preceding event: %w", eerr)
		}
		valid += int64(len(line) + 1)
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // Validated run ID and fixed journal name.
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	if err = f.Truncate(valid); err != nil {
		return errors.Join(err, f.Close())
	}
	if _, err = f.Seek(0, io.SeekEnd); err != nil {
		return errors.Join(err, f.Close())
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		return errors.Join(err, f.Close())
	}
	return errors.Join(f.Sync(), f.Close())
}

// Events ignores only an unterminated final record left by a crash.
func (s *Store) Events(ctx context.Context, id domain.RunID) (events []domain.Event, err error) {
	dir, err := s.run(id)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(dir, "events.jsonl")) //nolint:gosec // Validated run ID and constant journal name.
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open events: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return readEvents(ctx, bufio.NewReader(f))
}

func readEvents(ctx context.Context, r *bufio.Reader) ([]domain.Event, error) {
	var events []domain.Event
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return events, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read journal: %w", err)
		}
		var event domain.Event
		if err = json.Unmarshal(line, &event); err != nil {
			return nil, fmt.Errorf("decode journal: %w", err)
		}
		if event.Sequence != int64(len(events))+1 {
			return nil, fmt.Errorf("journal sequence gap")
		}
		events = append(events, event)
	}
}

// PutArtifact preserves content by digest; callers cannot overwrite evidence.
func (s *Store) PutArtifact(ctx context.Context, id domain.RunID, _ string, r io.Reader) (domain.Artifact, error) {
	var a domain.Artifact
	if err := ctx.Err(); err != nil {
		return a, err
	}
	dir, err := s.run(id)
	if err != nil {
		return a, err
	}
	dir = filepath.Join(dir, "artifacts")
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return a, fmt.Errorf("create artifact directory: %w", err)
	}
	return putStream(ctx, dir, r)
}

// List reads all checkpoints, failing on corruption instead of hiding a session.
func (s *Store) List(ctx context.Context) ([]domain.RunState, error) {
	entries, err := os.ReadDir(filepath.Join(s.home, "sessions"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var states []domain.RunState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		state, e := s.Load(ctx, domain.RunID(entry.Name()))
		if e != nil {
			return nil, e
		}
		states = append(states, state)
	}
	return states, nil
}
