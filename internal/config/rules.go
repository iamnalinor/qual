package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/iamnalinor/qual/internal/domain"
)

const (
	maxFileSize   = 256 * 1024
	maxRulesSize  = 2 * 1024 * 1024
	maxRulesDepth = 16
)

var revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Rule preserves applicability and source evidence separately from its content.
type Rule struct {
	Source   string `json:"source"`
	Revision string `json:"revision,omitempty"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Scope    string `json:"scope"`
	Glob     string `json:"glob,omitempty"`
	Content  string `json:"content"`
}

// Applies reports whether this instruction governs a repository-relative file.
func (r Rule) Applies(file string) bool {
	if !safePath(file) {
		return false
	}
	if r.Scope != "" && !strings.HasPrefix(file, r.Scope+"/") {
		return false
	}
	relative := strings.TrimPrefix(file, r.Scope+"/")
	if r.Glob == "" {
		return true
	}
	match, err := path.Match(r.Glob, relative)
	return err == nil && match
}

// Discovery includes bounded instruction data and nonfatal skipped-file warnings.
type Discovery struct {
	Rules    []Rule   `json:"rules"`
	Warnings []string `json:"warnings,omitempty"`
	total    int
}

// Discover reads repository instructions only at RulesRevision, plus personal rules.
func Discover(ctx context.Context, home string, s domain.Snapshot, extra []InstructionFile) (discovery Discovery, retErr error) {
	unlock, err := lockReadProject(ctx, home, string(s.ProjectID))
	if err != nil {
		return Discovery{}, err
	}
	defer func() { retErr = errors.Join(retErr, unlock()) }()
	return discoverUnlocked(ctx, home, s, extra, projectRoot(home, s))
}

func discoverUnlocked(ctx context.Context, home string, s domain.Snapshot, extra []InstructionFile, personalRoot string) (Discovery, error) {
	d := Discovery{}
	if s.RulesRevision != "" {
		if err := discoverGit(ctx, s, extra, &d); err != nil {
			return d, err
		}
	}
	roots := []struct{ root, source string }{{filepath.Join(home, "rules"), "personal_global"}}
	if s.ProjectID != "" {
		if !safeName(string(s.ProjectID)) {
			return d, invalid("invalid project identity")
		}
		roots = append(roots, struct{ root, source string }{filepath.Join(personalRoot, "rules"), "personal_project"})
	}
	for _, root := range roots {
		if err := discoverPersonal(ctx, root.root, root.source, &d); err != nil {
			return d, err
		}
	}
	return d, nil
}

func gitCommand(ctx context.Context, s domain.Snapshot, args ...string) ([]byte, error) {
	if s.StorePath == "" || !filepath.IsAbs(s.StorePath) || !revisionPattern.MatchString(s.RulesRevision) {
		return nil, invalid("rules require an absolute snapshot store and immutable commit")
	}
	// #nosec G204 -- Git receives discrete arguments and a validated immutable commit, never shell input.
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "--git-dir=" + gitDir(s.StorePath)}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0"}
	b, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, invalid("read fixed snapshot rules")
	}
	return b, nil
}

func gitDir(store string) string {
	if info, err := os.Stat(filepath.Join(store, ".git")); err == nil && info.IsDir() {
		return filepath.Join(store, ".git")
	}
	return store
}

func gitEntries(ctx context.Context, s domain.Snapshot) (map[string]string, error) {
	b, err := gitCommand(ctx, s, "ls-tree", "-rz", s.RulesRevision)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, entry := range strings.Split(string(b), "\x00") {
		parts := strings.SplitN(entry, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) == 3 {
			result[parts[1]] = fields[0]
		}
	}
	return result, nil
}

func gitFile(ctx context.Context, s domain.Snapshot, p string) ([]byte, error) {
	entries, err := gitEntries(ctx, s)
	if err != nil {
		return nil, err
	}
	mode, ok := entries[p]
	if !ok {
		return nil, nil
	}
	if mode != "100644" && mode != "100755" {
		return nil, invalid("configuration file is not a regular git blob")
	}
	size, err := gitCommand(ctx, s, "cat-file", "-s", s.RulesRevision+":"+p)
	if err != nil {
		return nil, err
	}
	var length int
	if _, err = fmt.Sscan(string(size), &length); err != nil || length > maxFileSize {
		return nil, invalid("configuration file exceeds size limit")
	}
	return gitCommand(ctx, s, "show", s.RulesRevision+":"+p)
}

func discoverGit(ctx context.Context, s domain.Snapshot, extra []InstructionFile, d *Discovery) error {
	entries, err := gitEntries(ctx, s)
	if err != nil {
		return err
	}
	selected := map[string]string{}
	for p := range entries {
		if supportedInstruction(p) {
			selected[p] = ""
		}
	}
	for _, f := range extra {
		if !safePath(f.Path) {
			return invalid("unsafe instruction path")
		}
		selected[f.Path] = f.Glob
	}
	paths := make([]string, 0, len(selected))
	for p := range selected {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := addGitRule(ctx, s, p, selected[p], d); err != nil {
			return err
		}
	}
	return nil
}

func supportedInstruction(p string) bool {
	switch path.Base(p) {
	case "AGENTS.md", "CLAUDE.md", "GEMINI.md":
		return true
	}
	return p == ".github/copilot-instructions.md" || strings.HasPrefix(p, ".qual/rules/")
}

func addGitRule(ctx context.Context, s domain.Snapshot, p, glob string, d *Discovery) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.Count(p, "/") > maxRulesDepth {
		d.warn("instruction depth exceeded: " + p)
		return nil
	}
	b, err := gitFile(ctx, s, p)
	if err != nil || b == nil {
		d.warn("instruction unavailable or oversized: " + p)
		return nil
	}
	scope := path.Dir(p)
	if scope == "." || strings.HasPrefix(p, ".qual/rules/") || p == ".github/copilot-instructions.md" {
		scope = ""
	}
	d.add(Rule{Source: "repository", Revision: s.RulesRevision, Path: p, Scope: scope, Glob: glob}, b)
	return nil
}

func (d *Discovery) add(r Rule, b []byte) {
	if len(b) > maxFileSize || d.total+len(b) > maxRulesSize {
		d.warn("instruction size limit: " + r.Path)
		return
	}
	hash := sha256.Sum256(b)
	r.SHA256 = hex.EncodeToString(hash[:])
	r.Content = string(b)
	d.total += len(b)
	d.Rules = append(d.Rules, r)
}

func discoverPersonal(ctx context.Context, root, source string, d *Discovery) error {
	if err := rejectSymlinks(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return invalid("personal rules path contains a symlink")
	}
	err := filepath.WalkDir(root, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		return personalEntry(p, relative, source, entry, d)
	})
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("discover personal rules: %w", err)
	}
	return nil
}

func personalEntry(p, relative, source string, entry fs.DirEntry, d *Discovery) error {
	if strings.Count(relative, string(filepath.Separator)) > maxRulesDepth {
		d.warn("instruction depth exceeded: " + relative)
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	if entry.Type()&os.ModeSymlink != 0 {
		d.warn("skipped symlink instruction: " + relative)
		return nil
	}
	if entry.IsDir() {
		return nil
	}
	if !entry.Type().IsRegular() {
		d.warn("skipped nonregular instruction: " + relative)
		return nil
	}
	b, err := readOptional(p)
	if err != nil {
		d.warn("instruction unavailable: " + relative)
		return nil
	}
	d.add(Rule{Source: source, Path: relative}, b)
	return nil
}

func (d *Discovery) warn(message string) {
	const maxWarnings = 128
	if len(d.Warnings) < maxWarnings {
		d.Warnings = append(d.Warnings, message)
	}
}
