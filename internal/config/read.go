package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamnalinor/qual/internal/domain"
)

func projectRoot(home string, s domain.Snapshot) string {
	return filepath.Join(home, "projects", string(s.ProjectID))
}

// Readers take the writer lock so settings and rules belong to one installed generation.
func lockReadProject(ctx context.Context, home, projectID string) (func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(home) {
		return nil, invalid("configuration home must be absolute")
	}
	if projectID == "" {
		return func() error { return nil }, nil
	}
	if !safeName(projectID) {
		return nil, invalid("invalid project identity")
	}
	root := filepath.Join(home, "projects", projectID)
	if err := rejectSymlinks(root); err != nil {
		return nil, invalid("symlink personal project path")
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return nil, fmt.Errorf("create project lock directory: %w", err)
	}
	unlock, err := lockProject(root)
	if err != nil {
		return nil, err
	}
	if err = recoverProject(root); err != nil {
		return nil, errors.Join(err, unlock())
	}
	return unlock, nil
}
