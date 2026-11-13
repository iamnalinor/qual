package auth

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
	"regexp"
	"syscall"
)

var profilePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func (s *Service) directory() string               { return filepath.Join(s.config.Home, "auth") }
func (s *Service) credentialPath(id string) string { return filepath.Join(s.directory(), id+".json") }

func (s *Service) ensureDirectory() error {
	if s.config.Home == "" {
		return authError("credential home is required")
	}
	if err := os.MkdirAll(s.directory(), 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	info, err := os.Lstat(s.directory())
	if err != nil {
		return fmt.Errorf("inspect credential directory: %w", err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(owner.Uid) != int64(os.Getuid()) || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return authError("credential directory must be owner-only and not a symlink")
	}
	return nil
}

// Save atomically stores one verified account, including rotating credentials.
func (s *Service) Save(ctx context.Context, c Credentials) (resultErr error) {
	lock, err := s.lockProfile(ctx, c.ID)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	return s.saveLocked(ctx, c)
}

func (s *Service) saveLocked(ctx context.Context, c Credentials) error {
	if !profilePattern.MatchString(c.ID) {
		return authError("invalid credential profile")
	}
	c.SchemaVersion = 1
	data, err := json.Marshal(c) //nolint:gosec // G117: credentials are deliberately serialized exclusively into owner-only storage or the selected keyring.
	if err != nil {
		return authError("encode credentials")
	}
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	if s.config.Keyring != nil {
		return s.config.Keyring.Set(ctx, c.ID, data)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = s.ensureDirectory(); err != nil {
		return err
	}
	return atomicPrivate(s.credentialPath(c.ID), data)
}

func atomicPrivate(path string, data []byte) (resultErr error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".credential-*")
	if err != nil {
		return fmt.Errorf("create credential transaction: %w", err)
	}
	name := file.Name()
	defer func() {
		if removeErr := os.Remove(name); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove credential transaction: %w", removeErr))
		}
	}()
	if _, err = file.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write credential transaction: %w", err), file.Close())
	}
	if err = file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync credential transaction: %w", err), file.Close())
	}
	if err = file.Close(); err != nil {
		return fmt.Errorf("close credential transaction: %w", err)
	}
	if err = os.Rename(name, path); err != nil {
		return fmt.Errorf("commit credential transaction: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

// Load reads one selected profile; unsafe paths and readable credential files fail.
func (s *Service) Load(ctx context.Context, id string) (Credentials, error) {
	if !profilePattern.MatchString(id) {
		return Credentials{}, authError("invalid credential profile")
	}
	if err := ctx.Err(); err != nil {
		return Credentials{}, err
	}
	var data []byte
	var err error
	if s.config.Keyring == nil {
		if err = s.ensureDirectory(); err != nil {
			return Credentials{}, err
		}
	}
	if s.config.Keyring != nil {
		data, err = s.config.Keyring.Get(ctx, id)
	} else {
		data, err = readPrivate(s.credentialPath(id))
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("load selected credentials: %w", err)
	}
	var c Credentials
	if json.Unmarshal(data, &c) != nil || c.SchemaVersion != 1 || c.ID != id {
		return Credentials{}, authError("invalid credential record")
	}
	return c, nil
}

func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect credential record: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, authError("credential record must be owner-only regular file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // G304: validated profile or fixed host-id confines this read; no-follow rejects symlinks.
	if err != nil {
		return nil, fmt.Errorf("open credential record: %w", err)
	}
	defer file.Close() //nolint:errcheck // The bounded read result determines success for this read-only credential file.
	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read credential record: %w", err)
	}
	return data, nil
}

// List exposes metadata only, preserving signed-in profiles without sharing consent.
func (s *Service) List(ctx context.Context) ([]Profile, error) {
	ids, err := s.profileIDs(ctx)
	if err != nil {
		return nil, err
	}
	profiles := make([]Profile, 0, len(ids))
	for _, id := range ids {
		c, loadErr := s.Load(ctx, id)
		if loadErr != nil {
			return nil, loadErr
		}
		profiles = append(profiles, Profile{ID: c.ID, Kind: c.Kind, Email: c.Email, Subject: c.Subject, ClientID: c.ClientID, SharingEnabled: hasScope(c.Scopes, SharingScope), ExpiresAt: c.ExpiresAt})
	}
	return profiles, nil
}

func (s *Service) profileIDs(ctx context.Context) ([]string, error) {
	if s.config.Keyring != nil {
		return s.config.Keyring.List(ctx)
	}
	entries, err := os.ReadDir(s.directory())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	var ids []string
	for _, entry := range entries {
		extension := filepath.Ext(entry.Name())
		id := entry.Name()[:len(entry.Name())-len(extension)]
		if extension == ".json" && profilePattern.MatchString(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// Logout removes only the explicitly selected local credential record.
func (s *Service) Logout(ctx context.Context, id string) (resultErr error) {
	if !profilePattern.MatchString(id) {
		return authError("invalid credential profile")
	}
	lock, err := s.lockProfile(ctx, id)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	if s.config.Keyring != nil {
		return s.config.Keyring.Delete(ctx, id)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(s.credentialPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove credentials: %w", err)
	}
	return nil
}

func (s *Service) hostID(ctx context.Context) (result string, resultErr error) {
	lock, err := s.lockProfile(ctx, "_host-registration")
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, lock.Close()) }()
	s.storageMu.Lock()
	defer s.storageMu.Unlock()
	if err := s.ensureDirectory(); err != nil {
		return "", err
	}
	path := filepath.Join(s.directory(), "host-id")
	data, err := readPrivate(path)
	if err == nil {
		return string(data), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	id, err := randomValue()
	if err != nil {
		return "", err
	}
	if err = atomicPrivate(path, []byte(id)); err != nil {
		return "", err
	}
	return id, nil
}

func profileID(kind, client, subject string) string {
	hash := sha256.Sum256([]byte(kind + "\x00" + client + "\x00" + subject))
	return hex.EncodeToString(hash[:16])
}

func syncDirectory(path string) (resultErr error) {
	directory, err := os.Open(path) //nolint:gosec // G304: directory is derived from the validated credential transaction path.
	if err != nil {
		return fmt.Errorf("open credential directory for sync: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, directory.Close()) }()
	if err = directory.Sync(); err != nil {
		return fmt.Errorf("sync credential directory: %w", err)
	}
	return nil
}
