package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamnalinor/qual/internal/domain"
)

// Artifact locates immutable saved content after checking its identifier and digest.
func (s *Store) Artifact(ctx context.Context, id domain.RunID, artifactID string) (domain.Artifact, error) {
	var a domain.Artifact
	if err := ctx.Err(); err != nil {
		return a, err
	}
	digest, err := hex.DecodeString(artifactID)
	if err != nil || len(digest) != sha256.Size {
		return a, fmt.Errorf("invalid artifact digest")
	}
	dir, err := s.run(id)
	if err != nil {
		return a, err
	}
	path := filepath.Join(dir, "artifacts", artifactID)
	data, err := os.ReadFile(path) //nolint:gosec // Validated digest and run ID confine this immutable artifact path.
	if err != nil {
		return a, fmt.Errorf("read artifact: %w", err)
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != artifactID {
		return a, fmt.Errorf("artifact digest mismatch")
	}
	return domain.Artifact{ID: artifactID, Path: path, SHA256: artifactID, Size: int64(len(data))}, nil
}
