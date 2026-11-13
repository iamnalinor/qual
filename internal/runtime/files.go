package runtime

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/report"
)

func listFiles(ctx context.Context, root, query string) (string, error) {
	var output strings.Builder
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || confidential(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		return inspectFile(root, path, entry, query, &output)
	})
	if err != nil {
		return "", fmt.Errorf("walk workspace: %w", err)
	}
	return output.String(), nil
}

func inspectFile(root, path string, entry fs.DirEntry, query string, output *strings.Builder) error {
	if confidential(path) || entry.Type()&os.ModeSymlink != 0 {
		return nil
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("relative file path: %w", err)
	}
	if query == "" {
		output.WriteString(relative + "\n")
		return nil
	}
	info, err := entry.Info()
	if err != nil {
		return fmt.Errorf("file info: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 4*1024*1024 {
		return nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // Enumerated regular file; symlinks and credential names are excluded.
	if err != nil {
		return fmt.Errorf("search file: %w", err)
	}
	for line, text := range strings.Split(string(data), "\n") {
		if strings.Contains(text, query) {
			fmt.Fprintf(output, "%s:%d:%s\n", relative, line+1, text)
		}
	}
	return nil
}

type artifactReader interface {
	Artifact(context.Context, domain.RunID, string) (domain.Artifact, error)
}

func (r *Runtime) readArtifact(ctx context.Context, id domain.RunID, data []byte) (string, error) {
	var a struct {
		ID     string `json:"id"`
		Offset int64  `json:"offset"`
		Limit  int64  `json:"limit"`
	}
	if err := report.Decode(data, &a); err != nil {
		return "", err
	}
	store, ok := r.config.Store.(artifactReader)
	if !ok {
		return "", coded(domain.ErrUnsupportedCapability, "store does not support artifact range reads", nil)
	}
	artifact, err := store.Artifact(ctx, id, a.ID)
	if err != nil {
		return "", err
	}
	return readRange(artifact.Path, a.Offset, a.Limit)
}
