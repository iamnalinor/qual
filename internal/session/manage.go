package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/report"
)

// RequestCancel persists a signal that an owner in another process can observe.
func (s *Store) RequestCancel(ctx context.Context, id domain.RunID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.run(id)
	if err != nil {
		return err
	}
	return atomicWrite(dir, "cancel", []byte("cancel\n"))
}

// Cancelled checks a durable cancellation signal without taking the owner lock.
func (s *Store) Cancelled(ctx context.Context, id domain.RunID) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	dir, err := s.run(id)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(filepath.Join(dir, "cancel"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ClearCancel is used only by an explicit resume after ownership is acquired.
func (s *Store) ClearCancel(ctx context.Context, id domain.RunID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.run(id)
	if err != nil {
		return err
	}
	err = os.Remove(filepath.Join(dir, "cancel"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Export writes a redacted diagnostic JSON archive. Private resume files are unchanged.
// ArtifactSHA256 records the exported digest when a legacy transcript was redacted.
func (s *Store) Export(ctx context.Context, id domain.RunID, w io.Writer) error {
	state, err := s.Load(ctx, id)
	if err != nil {
		return err
	}
	events, err := s.Events(ctx, id)
	if err != nil {
		return err
	}
	for i := range events {
		visible, _, redactErr := report.RedactProviderData(events[i].Data)
		if redactErr != nil {
			return redactErr
		}
		events[i].Data = visible
	}
	dir, err := s.run(id)
	if err != nil {
		return err
	}
	artifacts, err := exportArtifacts(ctx, filepath.Join(dir, "artifacts"))
	if err != nil {
		return err
	}
	digests, redacted, err := redactArtifacts(artifacts)
	if err != nil {
		return err
	}
	archive := struct {
		SchemaVersion     int               `json:"schema_version"`
		State             domain.RunState   `json:"state"`
		Events            []domain.Event    `json:"events"`
		Artifacts         map[string][]byte `json:"artifacts"`
		ArtifactSHA256    map[string]string `json:"artifact_sha256"`
		RedactedArtifacts []string          `json:"redacted_artifacts,omitempty"`
	}{1, state, events, artifacts, digests, redacted}
	if err = json.NewEncoder(w).Encode(archive); err != nil {
		return fmt.Errorf("encode session export: %w", err)
	}
	return nil
}

func exportArtifacts(ctx context.Context, dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list export artifacts: %w", err)
	}
	result := map[string][]byte{}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() {
			continue
		}
		if !entry.Type().IsRegular() {
			continue
		}
		data, e := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // Enumerated regular artifact entry within a validated session directory.
		if e != nil {
			return nil, fmt.Errorf("read exported artifact: %w", e)
		}
		result[entry.Name()] = data
	}
	return result, nil
}

// Remove deletes only an unlocked terminal session; source worktree cleanup belongs to Source.
func (s *Store) Remove(ctx context.Context, id domain.RunID) (err error) {
	unlock, err := s.Lock(ctx, id)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	state, err := s.Load(ctx, id)
	if err != nil {
		return err
	}
	if state.Status == domain.StatusRunning || state.Status == domain.StatusPending {
		return &domain.Error{Code: domain.ErrIncomplete, Message: "cannot remove an active session"}
	}
	dir, err := s.run(id)
	if err != nil {
		return err
	}
	if err = os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove session: %w", err)
	}
	return nil
}

func redactArtifacts(artifacts map[string][]byte) (map[string]string, []string, error) {
	digests := map[string]string{}
	var redacted []string
	for id, data := range artifacts {
		visible, changed, err := report.RedactProviderData(data)
		if err != nil {
			return nil, nil, err
		}
		artifacts[id] = visible
		sum := sha256.Sum256(visible)
		digests[id] = hex.EncodeToString(sum[:])
		if changed {
			redacted = append(redacted, id)
		}
	}
	return digests, redacted, nil
}
