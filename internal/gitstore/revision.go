package gitstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamnalinor/qual/internal/domain"
)

// ImportRevision retains one fixed commit/tree closure for trusted target rules.
// Existing snapshot refs and shallow boundaries are preserved.
func (s *Source) ImportRevision(ctx context.Context, snapshot domain.Snapshot, repository, revision string) error {
	if !objectID(revision) {
		return failure("invalid_config", "revision must be a full object ID", nil)
	}
	original, err := os.ReadFile(filepath.Join(snapshot.StorePath, "shallow"))
	if err != nil {
		return failure("source_unavailable", "read snapshot boundaries", err)
	}
	if err = copyObjects(ctx, repository, snapshot.StorePath, revision, "", nil); err != nil {
		return err
	}
	boundaries := strings.Fields(string(original))
	found := false
	for _, id := range boundaries {
		found = found || id == revision
	}
	if !found {
		boundaries = append(boundaries, revision)
	}
	// #nosec G703 -- snapshot.StorePath is a Qual-owned store supplied by the trusted application.
	if err = os.WriteFile(filepath.Join(snapshot.StorePath, "shallow"), []byte(strings.Join(boundaries, "\n")+"\n"), 0o600); err != nil {
		return failure("source_unavailable", "retain revision boundaries", err)
	}
	_, err = git(ctx, snapshot.StorePath, nil, nil, "update-ref", "refs/qual/rules", revision)
	return err
}
