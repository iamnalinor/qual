package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"gopkg.in/yaml.v3"
)

// Proposal is untrusted setup output; settings cannot grant privileged capabilities.
type Proposal struct {
	Settings []byte            `json:"settings"`
	Rules    map[string]string `json:"rules"`
}

// Preview validates a proposal and returns safe structured content for approval.
func Preview(p Proposal) ([]byte, error) {
	settings, err := proposalSettings(p)
	if err != nil {
		return nil, err
	}
	total := 0
	for name, content := range p.Rules {
		total += len(content)
		if total > maxRulesSize {
			return nil, invalid("combined proposed rules exceed size limit")
		}
		if !safePath(name) || len(content) > maxFileSize {
			return nil, invalid("unsafe or oversized proposed rule")
		}
	}
	b, err := yaml.Marshal(struct {
		Settings map[string]any    `yaml:"settings"`
		Rules    map[string]string `yaml:"rules"`
	}{settings, p.Rules})
	if err != nil {
		return nil, fmt.Errorf("encode proposal preview: %w", err)
	}
	return b, nil
}

// ApplyRequest carries acceptance and the caller-selected personal trust designation.
type ApplyRequest struct {
	Home      string
	Context   Request
	ProjectID string
	Proposal  Proposal
	Accepted  bool
	Trust     string
}

func proposalSettings(p Proposal) (map[string]any, error) {
	values := map[string]any{}
	if len(p.Settings) == 0 {
		return values, nil
	}
	var typed Settings
	if err := decode(p.Settings, &typed); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(p.Settings, &values); err != nil {
		return nil, invalid("invalid proposal settings")
	}
	if err := checkRestricted(values); err != nil {
		return nil, err
	}
	return values, nil
}

// Apply installs accepted personal project changes under an exclusive lock, with rollback.
func Apply(ctx context.Context, r ApplyRequest) (retErr error) {
	if !r.Accepted {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.Context.Home == "" {
		r.Context.Home = r.Home
	}
	if err := validateApplyRequest(r); err != nil {
		return err
	}
	root := filepath.Join(r.Home, "projects", r.ProjectID)
	if err := rejectSymlinks(root); err != nil {
		return invalid("symlink project destination")
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return fmt.Errorf("create projects: %w", err)
	}
	unlock, err := lockProject(root)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, unlock()) }()
	if err = recoverProject(root); err != nil {
		return err
	}
	return applyLocked(ctx, r, root)
}

func applyLocked(ctx context.Context, r ApplyRequest, root string) (retErr error) {
	stage, err := os.MkdirTemp(filepath.Dir(root), ".config-stage-")
	if err != nil {
		return fmt.Errorf("stage configuration: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, os.RemoveAll(stage)) }()
	if err = copyProject(ctx, root, stage); err != nil {
		return err
	}
	if err = stageProposal(r, stage); err != nil {
		return err
	}
	if err = validateStaged(ctx, r, stage); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return installProject(stage, root)
}

func installProject(stage, root string) error {
	backup := root + ".config.backup"
	exists := false
	if _, err := os.Lstat(root); err == nil {
		if err = os.Rename(root, backup); err != nil {
			return fmt.Errorf("backup configuration: %w", err)
		}
		exists = true
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect project configuration: %w", err)
	}
	if err := os.Rename(stage, root); err != nil {
		return rollbackProject(root, backup, exists, err)
	}
	if exists {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("remove configuration backup: %w", err)
		}
	}
	return nil
}

func rollbackProject(root, backup string, exists bool, installErr error) error {
	if exists {
		if err := os.Rename(backup, root); err != nil {
			return fmt.Errorf("install configuration: %w; rollback: %w", installErr, err)
		}
	}
	return fmt.Errorf("install configuration: %w", installErr)
}

func copyProject(ctx context.Context, root, stage string) (retErr error) {
	source, err := os.OpenRoot(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open existing project: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, source.Close()) }()
	err = filepath.WalkDir(root, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return invalid("symlink in personal project configuration")
		}
		return copyEntry(source, root, stage, p, entry)
	})
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stage existing project data: %w", err)
	}
	return nil
}

func stageProposal(r ApplyRequest, stage string) error {
	settingsPath := filepath.Join(stage, "settings.yaml")
	existing, err := readOptional(settingsPath)
	if err != nil {
		return err
	}
	merged, err := existingSettings(existing)
	if err != nil {
		return err
	}
	proposed, err := proposalSettings(r.Proposal)
	if err != nil {
		return err
	}
	prior, err := proposalAuthorization(r.Home, stage, merged)
	if err != nil {
		return err
	}
	if _, err = retainReviewerCaps(authorizationValues(prior), proposed); err != nil {
		return err
	}
	merge(merged, proposed, "", Origin{}, map[string]Origin{})
	if r.Trust != "" {
		merged["trust"] = r.Trust
	}
	if err = finalizeReviewerAuthorization(stage, prior, merged); err != nil {
		return err
	}
	data, err := yaml.Marshal(merged)
	if err != nil {
		return fmt.Errorf("encode accepted settings: %w", err)
	}
	if err = os.WriteFile(settingsPath, data, 0o600); err != nil {
		return fmt.Errorf("stage accepted settings: %w", err)
	}
	return stageRules(stage, r.Proposal.Rules)
}

func existingSettings(data []byte) (map[string]any, error) {
	merged := map[string]any{}
	if len(data) == 0 {
		return merged, nil
	}
	var typed Settings
	if err := decode(data, &typed); err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, &merged); err != nil {
		return nil, invalid("invalid existing settings")
	}
	return merged, nil
}

func stageRules(stage string, rules map[string]string) error {
	for name, content := range rules {
		p := filepath.Join(stage, "rules", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return fmt.Errorf("stage rules directory: %w", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			return fmt.Errorf("stage accepted rules: %w", err)
		}
	}
	return nil
}

func persistentContext(req Request) Request {
	req.CLI = nil
	req.TrustedCI = nil
	req.Environment = nil
	return req
}

func validateStaged(ctx context.Context, r ApplyRequest, stage string) error {
	if _, err := resolveUnlocked(ctx, persistentContext(r.Context), stage); err != nil {
		return err
	}
	_, err := resolveUnlocked(ctx, r.Context, stage)
	return err
}

func validateApplyRequest(r ApplyRequest) error {
	if r.Context.Home != r.Home {
		return invalid("proposal context home differs from destination")
	}
	if string(r.Context.Snapshot.ProjectID) != r.ProjectID {
		return invalid("proposal requires the prepared snapshot for the destination project")
	}

	if !filepath.IsAbs(r.Home) {
		return invalid("configuration home must be absolute")
	}
	if !safeName(r.ProjectID) {
		return invalid("invalid project identity")
	}
	if r.Trust != "" && r.Trust != "trusted" && r.Trust != "untrusted" {
		return invalid("invalid personal trust")
	}
	_, err := Preview(r.Proposal)
	return err
}

func copyEntry(source *os.Root, root, stage, p string, entry fs.DirEntry) error {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return err
	}
	dest := filepath.Join(stage, rel)
	if entry.IsDir() {
		return os.MkdirAll(dest, 0o700)
	}
	if !entry.Type().IsRegular() {
		return invalid("nonregular personal project file")
	}
	b, err := source.ReadFile(rel)
	if err != nil {
		return fmt.Errorf("read existing project data: %w", err)
	}
	// #nosec G703 -- Destination is a fresh private staging tree and rel comes from WalkDir under root.
	return os.WriteFile(dest, b, 0o600)
}

func lockProject(root string) (func() error, error) {
	// #nosec G304 -- Root uses a validated project ID; O_NOFOLLOW rejects symlink lock files.
	lock, err := os.OpenFile(root+".config.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open project configuration lock: %w", err)
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(fmt.Errorf("acquire project configuration lock: %w", err), lock.Close())
	}
	return func() error { return errors.Join(syscall.Flock(int(lock.Fd()), syscall.LOCK_UN), lock.Close()) }, nil
}

func recoverProject(root string) error {
	backup := root + ".config.backup"
	if err := rejectSymlinks(backup); err != nil {
		return invalid("symlink configuration backup")
	}
	if _, err := os.Lstat(backup); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect configuration backup: %w", err)
	}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		if err = os.Rename(backup, root); err != nil {
			return fmt.Errorf("recover configuration backup: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect active configuration: %w", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("clean previous configuration backup: %w", err)
	}
	return nil
}
