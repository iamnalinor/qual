package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/iamnalinor/qual/internal/domain"
)

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func putStream(ctx context.Context, dir string, r io.Reader) (artifact domain.Artifact, err error) {
	file, err := os.CreateTemp(dir, ".artifact-")
	if err != nil {
		return artifact, fmt.Errorf("create artifact: %w", err)
	}
	path := file.Name()
	defer func() {
		removeErr := os.Remove(path)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	hash := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hash), contextReader{ctx: ctx, reader: r})
	if err != nil {
		return artifact, errors.Join(fmt.Errorf("copy artifact: %w", err), file.Close())
	}
	if err = file.Sync(); err != nil {
		return artifact, errors.Join(fmt.Errorf("sync artifact: %w", err), file.Close())
	}
	if err = file.Close(); err != nil {
		return artifact, fmt.Errorf("close artifact: %w", err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	target := filepath.Join(dir, digest)
	if err = os.Rename(path, target); err != nil {
		return artifact, fmt.Errorf("preserve artifact: %w", err)
	}
	if err = syncDirectory(dir); err != nil {
		return artifact, err
	}
	return domain.Artifact{ID: digest, Path: target, SHA256: digest, Size: size}, nil
}

func syncDirectory(dir string) error {
	file, err := os.Open(dir) //nolint:gosec // Caller supplies a validated session directory.
	if err != nil {
		return fmt.Errorf("open directory: %w", err)
	}
	return errors.Join(file.Sync(), file.Close())
}
