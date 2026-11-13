// Package gitstore captures independent, immutable Git review inputs.
package gitstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/iamnalinor/qual/internal/diagnostic"
	"github.com/iamnalinor/qual/internal/domain"
)

// Source prepares snapshots and worktrees in Qual-owned storage.
type Source struct{ home string }

// New constructs a local Git source adapter.
func New(home string) *Source {
	if home != "" {
		if resolved, e := canonicalDestination(home); e == nil {
			home = resolved
		}
	}
	return &Source{home: home}
}

func failure(code, message string, err error) error {
	return &domain.Error{Code: domain.ErrorCode(code), Message: message, Cause: err}
}

// git excludes ambient Git routing, optional index writes, global config and hooks.
func git(ctx context.Context, dir string, input []byte, extra []string, args ...string) ([]byte, error) {
	var output bytes.Buffer
	err := runGit(ctx, dir, bytes.NewReader(input), &output, extra, args...)
	return output.Bytes(), err
}

func runGit(ctx context.Context, dir string, input io.Reader, output io.Writer, extra []string, args ...string) error {
	// #nosec G204 -- executable is fixed; adapter supplies Git plumbing arguments, never shell code.
	c := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)...)
	c.Dir = dir
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			c.Env = append(c.Env, v)
		}
	}
	c.Env = append(c.Env, "GIT_NO_LAZY_FETCH=1", "GIT_NO_REPLACE_OBJECTS=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Qual", "GIT_AUTHOR_EMAIL=qual@localhost", "GIT_COMMITTER_NAME=Qual", "GIT_COMMITTER_EMAIL=qual@localhost")
	c.Env = append(c.Env, extra...)
	c.Stdin = input
	var stderr bytes.Buffer
	c.Stdout = output
	c.Stderr = &stderr
	if e := c.Run(); e != nil {
		return failure("source_unavailable", "git "+args[0]+" failed", diagnostic.GitCause(e, stderr.String(), c.Env))
	}
	return nil
}

func value(ctx context.Context, dir string, args ...string) (string, error) {
	b, e := git(ctx, dir, nil, nil, args...)
	return strings.TrimSpace(string(b)), e
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func validID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\\x00")
}

// Prepare captures local commits, index contents, or tracked working files.
func (s *Source) Prepare(ctx context.Context, req domain.SourceRequest) (result domain.Snapshot, err error) {
	defer func() { err = sourceError(err) }()
	work, e := workingMode(req)
	if e != nil {
		return domain.Snapshot{}, e
	}
	target := req.Target
	if target == "" {
		target = "."
	}
	root, e := value(ctx, target, "rev-parse", "--show-toplevel")
	if e != nil {
		return domain.Snapshot{}, e
	}
	head, e := sourceHead(ctx, root)
	if e != nil {
		return domain.Snapshot{}, e
	}
	base, e := resolveOr(ctx, root, req.Base, head)
	if e != nil {
		return domain.Snapshot{}, e
	}
	right, e := resolveOr(ctx, root, req.Head, head)
	if e != nil {
		return domain.Snapshot{}, e
	}
	if !req.Staged && !work {
		return s.Import(ctx, req, root, base, right)
	}
	return s.captureStable(ctx, req, root, base, head)
}

func workingMode(req domain.SourceRequest) (bool, error) {
	selectors := 0
	for _, selected := range []bool{req.Staged, req.Worktree, req.Head != ""} {
		if selected {
			selectors++
		}
	}
	work := req.Worktree || (!req.Staged && req.Base == "" && req.Head == "")
	if selectors > 1 || (req.Head != "" && req.Base == "") || (req.IncludeUntracked && !work) {
		return false, failure("invalid_config", "incompatible local selectors", nil)
	}
	return work, nil
}

func sourceHead(ctx context.Context, root string) (string, error) {
	head, e := value(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if e == nil {
		return head, nil
	}
	if _, branchErr := value(ctx, root, "symbolic-ref", "HEAD"); branchErr != nil {
		return "", e
	}
	return "", nil
}

func resolveOr(ctx context.Context, root, ref, fallback string) (string, error) {
	if ref == "" {
		return fallback, nil
	}
	return value(ctx, root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
}

func (s *Source) captureStable(ctx context.Context, req domain.SourceRequest, root, base, head string) (domain.Snapshot, error) {
	for attempt := 0; attempt < 3; attempt++ {
		first, e := capture(ctx, root, req.Staged, req.IncludeUntracked)
		if e != nil {
			return domain.Snapshot{}, e
		}

		snap, stable, e := s.tryCapture(ctx, req, root, base, head, first)
		if e != nil {
			return domain.Snapshot{}, e
		}
		if stable {
			return snap, nil
		}

	}
	return domain.Snapshot{}, failure("source_changed", "selected source changed during three captures", nil)
}

func (s *Source) tryCapture(ctx context.Context, req domain.SourceRequest, root, base, head string, first []entry) (domain.Snapshot, bool, error) {
	if req.AllowEmpty {
		return s.captureAttempt(ctx, req, root, base, head, first)
	}
	same, e := selectedMatchesBase(ctx, root, base, first)
	if e != nil {
		return domain.Snapshot{}, false, e
	}
	if !same {
		return s.captureAttempt(ctx, req, root, base, head, first)
	}
	stable, e := unchanged(ctx, root, head, req, first)
	if e != nil {
		return domain.Snapshot{}, false, e
	}
	if stable {
		return domain.Snapshot{}, false, failure("no_changes", "selected trees are identical", nil)
	}
	return domain.Snapshot{}, false, nil
}

func unchanged(ctx context.Context, root, head string, req domain.SourceRequest, first []entry) (bool, error) {
	second, e := capture(ctx, root, req.Staged, req.IncludeUntracked)
	if e != nil {
		return false, e
	}
	current, e := sourceHead(ctx, root)
	return equalEntries(first, second) && current == head, e
}

func (s *Source) captureAttempt(ctx context.Context, req domain.SourceRequest, root, base, head string, first []entry) (domain.Snapshot, bool, error) {
	snap, e := s.build(ctx, req, root, base, head, first)
	if e != nil {
		return domain.Snapshot{}, false, e
	}
	return finishCapture(ctx, req, snap, root, head, first)
}

func finishCapture(ctx context.Context, req domain.SourceRequest, snap domain.Snapshot, root, head string, first []entry) (result domain.Snapshot, stable bool, err error) {
	defer func() {
		if err != nil || !stable {
			err = errors.Join(err, os.RemoveAll(snap.StorePath))
		}
	}()
	stable, e := unchanged(ctx, root, head, req, first)
	if e != nil {
		return domain.Snapshot{}, false, e
	}
	return snap, stable, nil
}

type entry struct {
	mode, oid, path string
	data            []byte
}

func capture(ctx context.Context, root string, staged, untracked bool) ([]entry, error) {
	entries, e := indexEntries(ctx, root)
	if e != nil {
		return nil, e
	}
	if !staged {
		if e = workingEntries(ctx, root, untracked, entries); e != nil {
			return nil, e
		}
	}
	result := make([]entry, 0, len(entries))
	for _, item := range entries {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].path < result[j].path })
	return result, nil
}

func indexEntries(ctx context.Context, root string) (map[string]entry, error) {
	b, e := git(ctx, root, nil, nil, "ls-files", "--stage", "-z")
	if e != nil {
		return nil, e
	}
	entries := map[string]entry{}
	for _, line := range bytes.Split(b, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		left, path, ok := strings.Cut(string(line), "\t")
		fields := strings.Fields(left)
		if !ok || len(fields) != 3 {
			return nil, failure("source_unavailable", "invalid index entry", nil)
		}
		if fields[2] != "0" {
			return nil, failure("unmerged_index", "resolve index conflicts before review", nil)
		}
		entries[path] = entry{mode: fields[0], oid: fields[1], path: path}
	}
	return entries, nil
}

func workingEntries(ctx context.Context, root string, untracked bool, entries map[string]entry) (err error) {
	files, e := os.OpenRoot(root)
	if e != nil {
		return failure("source_unavailable", "open source root", e)
	}
	defer func() { err = errors.Join(err, files.Close()) }()
	if untracked {
		if e = addUntracked(ctx, root, entries); e != nil {
			return e
		}
	}
	for path, item := range entries {
		if item.mode == "160000" {
			continue
		}
		if e = ctx.Err(); e != nil {
			return e
		}
		item, present, e := readSelected(files, item)
		if e != nil {
			return e
		}
		if !present {
			delete(entries, path)
			continue
		}

		entries[path] = item
	}
	return nil
}

func readSelected(files *os.Root, item entry) (entry, bool, error) {
	info, e := files.Lstat(item.path)
	if os.IsNotExist(e) || errors.Is(e, syscall.ENOTDIR) {
		return item, false, nil
	}
	if e != nil {
		return item, false, failure("source_unavailable", "inspect tracked file", e)
	}
	if info.IsDir() {
		return item, false, nil
	}
	result, e := readWorking(files, item, info)
	return result, true, e
}

func addUntracked(ctx context.Context, root string, entries map[string]entry) error {
	b, e := git(ctx, root, nil, nil, "ls-files", "--others", "--exclude-standard", "-z")
	if e != nil {
		return e
	}
	for _, path := range bytes.Split(b, []byte{0}) {
		if len(path) > 0 {
			entries[string(path)] = entry{path: string(path)}
		}
	}
	return nil
}

func readWorking(files *os.Root, item entry, info os.FileInfo) (entry, error) {
	item.oid = ""
	var e error
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		var link string
		link, e = files.Readlink(item.path)
		item.data = []byte(link)
		item.mode = "120000"
	case info.Mode().IsRegular():
		item.data, e = files.ReadFile(item.path)
		item.mode = "100644"
		if info.Mode()&0o111 != 0 {
			item.mode = "100755"
		}
	default:
		return item, failure("source_unavailable", "unsupported file type: "+item.path, nil)
	}
	if e != nil {
		return item, failure("source_unavailable", "read tracked file", e)
	}
	return item, nil
}

func equalEntries(a, b []entry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].path != b[i].path || a[i].mode != b[i].mode || a[i].oid != b[i].oid || !bytes.Equal(a[i].data, b[i].data) {
			return false
		}
	}
	return true
}

// Import captures an already fetched repository using immutable commit IDs.
// It copies objects through a pack, never alternates or source hardlinks.
func (s *Source) Import(ctx context.Context, req domain.SourceRequest, repository, baseCommit, headCommit string) (result domain.Snapshot, err error) {
	defer func() { err = sourceError(err) }()
	for _, commit := range []string{baseCommit, headCommit} {
		if !objectID(commit) {
			return domain.Snapshot{}, failure("invalid_config", "Import requires immutable full commit IDs", nil)
		}
		kind, e := value(ctx, repository, "cat-file", "-t", commit)
		if e != nil {
			return domain.Snapshot{}, e
		}
		if kind != "commit" {
			return domain.Snapshot{}, failure("invalid_config", "Import requires commit objects", nil)
		}
	}
	if !req.AllowEmpty {
		left, e := commitTree(ctx, repository, baseCommit)
		if e != nil {
			return domain.Snapshot{}, e
		}
		right, e := commitTree(ctx, repository, headCommit)
		if e != nil {
			return domain.Snapshot{}, e
		}
		if left == right {
			return domain.Snapshot{}, failure("no_changes", "selected trees are identical", nil)
		}
	}
	return s.build(ctx, req, repository, baseCommit, headCommit, nil)
}

func objectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	_, e := hex.DecodeString(id)
	return e == nil
}

func (s *Source) build(ctx context.Context, req domain.SourceRequest, root, base, head string, entries []entry) (result domain.Snapshot, err error) {
	var snap domain.Snapshot
	store, e := s.newStore(ctx, req, root)
	if e != nil {
		return snap, e
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(store))
		}
	}()
	if e = copyObjects(ctx, root, store, base, head, entries); e != nil {
		return snap, e
	}
	baseTree, e := commitTree(ctx, store, base)
	if e != nil {
		return snap, e
	}
	tree, e := rightTree(ctx, store, head, entries)
	if e != nil {
		return snap, e
	}
	if !req.AllowEmpty && baseTree == tree {
		return snap, failure("no_changes", "selected trees are identical", nil)
	}
	return makeSnapshot(ctx, req, root, store, base, head, baseTree, tree, entries)
}

func makeSnapshot(ctx context.Context, req domain.SourceRequest, root, store, base, head, baseTree, tree string, entries []entry) (domain.Snapshot, error) {
	var snap domain.Snapshot
	args := []string{"commit-tree", tree}
	if head != "" {
		args = append(args, "-p", head)
	}
	b, e := git(ctx, store, []byte("Qual immutable snapshot\n"), nil, args...)
	if e != nil {
		return snap, e
	}
	commit := strings.TrimSpace(string(b))
	if _, e = git(ctx, store, nil, nil, "update-ref", "refs/heads/snapshot", commit); e != nil {
		return snap, e
	}
	diff, e := git(ctx, store, nil, nil, "diff", "--no-ext-diff", "--no-textconv", "--binary", baseTree, tree)
	if e != nil {
		return snap, e
	}
	manifest, e := changedFiles(ctx, store, baseTree, tree)
	if e != nil {
		return snap, e
	}
	kind := snapshotKind(req, entries)
	identity, e := projectIdentity(ctx, root)
	if e != nil {
		return snap, e
	}
	snap = domain.Snapshot{SchemaVersion: 1, RunID: req.RunID, ProjectID: domain.ProjectID(digest([]byte(identity))), Kind: kind, Repository: root, Root: root, StorePath: store, BaseCommit: base, HeadCommit: commit, TargetCommit: head, BaseTree: baseTree, HeadTree: tree, DiffHash: digest(diff), RulesRevision: head, Files: manifest, CapturedAt: time.Now().UTC()}
	snap.OriginalHead = head
	if kind == "commits" {
		snap.RulesRevision = base
	}
	meta := persistedCapture{Head: head, Staged: req.Staged, Untracked: req.IncludeUntracked, Fingerprint: fingerprint(entries)}
	data, e := json.Marshal(meta)
	if e != nil {
		return snap, e
	}
	if e = os.WriteFile(filepath.Join(store, "qual-source.json"), data, 0o600); e != nil {
		return snap, e
	}
	return snap, nil
}

func changedFiles(ctx context.Context, store, base, head string) ([]domain.ChangedFile, error) {
	b, e := git(ctx, store, nil, nil, "diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z", "--no-renames", base, head)
	if e != nil {
		return nil, e
	}
	fields := bytes.Split(b, []byte{0})
	out := []domain.ChangedFile{}
	for i := 0; i+1 < len(fields) && len(fields[i]) > 0; i += 2 {
		out = append(out, domain.ChangedFile{Status: string(fields[i]), Path: string(fields[i+1])})
	}
	b, e = git(ctx, store, nil, nil, "diff", "--no-ext-diff", "--no-textconv", "--numstat", "-z", "--no-renames", base, head)
	if e != nil {
		return nil, e
	}
	binary := map[string]bool{}
	for _, line := range bytes.Split(b, []byte{0}) {
		parts := bytes.SplitN(line, []byte{'\t'}, 3)
		if len(parts) == 3 && string(parts[0]) == "-" {
			binary[string(parts[2])] = true
		}
	}
	gitlinks, e := submodulePaths(ctx, store, base, head)
	if e != nil {
		return nil, e
	}
	for i := range out {
		out[i].Binary = binary[out[i].Path]
		if gitlinks[out[i].Path] {
			out[i].ExcludedReason = "submodule_not_expanded"
		}
	}
	return out, nil
}

func (s *Source) newStore(ctx context.Context, req domain.SourceRequest, root string) (result string, err error) {
	home, e := s.storageHome(req, root)
	if e != nil {
		return "", e
	}
	parent := filepath.Join(home, "snapshots", string(req.RunID))
	if e = os.MkdirAll(parent, 0o700); e != nil {
		return "", e
	}
	store, e := os.MkdirTemp(parent, "repo-")
	if e != nil {
		return "", e
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(store))
		}
	}()
	format, e := value(ctx, root, "rev-parse", "--show-object-format")
	if e != nil {
		return "", e
	}
	if _, e = git(ctx, store, nil, nil, "init", "--bare", "--object-format="+format, "."); e != nil {
		return "", e
	}
	return store, nil
}

func copyObjects(ctx context.Context, root, store, base, head string, entries []entry) (err error) {
	objects, e := selectedObjects(ctx, root, base, head, entries)
	if e != nil {
		return e
	}
	if len(objects) == 0 {
		return nil
	}
	pack, e := os.CreateTemp(store, "import-*.pack")
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, pack.Close(), os.Remove(pack.Name())) }()
	if e = runGit(ctx, root, strings.NewReader(strings.Join(objects, "\n")+"\n"), pack, nil, "pack-objects", "--stdout"); e != nil {
		return e
	}
	if _, e = pack.Seek(0, io.SeekStart); e != nil {
		return e
	}
	if e = runGit(ctx, store, pack, io.Discard, nil, "index-pack", "--stdin"); e != nil {
		return e
	}
	return shallowBoundaries(store, base, head)
}

// No-walk excludes commit ancestors while --objects still enumerates the
// selected commits' recursive trees and blobs. pack-objects receives fixed IDs.
func selectedObjects(ctx context.Context, root, base, head string, entries []entry) ([]string, error) {
	objects := []string{}
	for _, commit := range []string{base, head} {
		if commit == "" {
			continue
		}
		b, e := git(ctx, root, nil, nil, "rev-list", "--objects", "--no-walk", "--no-object-names", commit, "--")
		if e != nil {
			return nil, e
		}
		objects = append(objects, strings.Fields(string(b))...)
	}
	for _, item := range entries {
		if item.oid != "" && item.mode != "160000" {
			objects = append(objects, item.oid)
		}
	}
	return objects, nil
}

// Imported commits terminate history traversal because their parents are not
// part of the selected snapshot closure.
func shallowBoundaries(store, base, head string) error {
	commits := []string{}
	for _, commit := range []string{base, head} {
		if commit != "" && (len(commits) == 0 || commits[0] != commit) {
			commits = append(commits, commit)
		}
	}
	if len(commits) == 0 {
		return nil
	}
	return os.WriteFile(filepath.Join(store, "shallow"), []byte(strings.Join(commits, "\n")+"\n"), 0o600)
}

func selectedMatchesBase(ctx context.Context, root, base string, entries []entry) (bool, error) {
	if base == "" {
		return len(entries) == 0, nil
	}
	b, e := git(ctx, root, nil, nil, "ls-tree", "-r", "-z", base+"^{tree}")
	if e != nil {
		return false, e
	}
	expected := map[string]string{}
	for _, line := range bytes.Split(b, []byte{0}) {
		left, path, ok := strings.Cut(string(line), "\t")
		if ok {
			fields := strings.Fields(left)
			expected[path] = fields[0] + " " + fields[2]
		}
	}
	if len(entries) != len(expected) {
		return false, nil
	}
	for _, item := range entries {
		oid := item.oid
		if oid == "" {
			data, e := git(ctx, root, item.data, nil, "hash-object", "--stdin")
			if e != nil {
				return false, e
			}
			oid = strings.TrimSpace(string(data))
		}
		if expected[item.path] != item.mode+" "+oid {
			return false, nil
		}
	}
	return true, nil
}

func commitTree(ctx context.Context, store, commit string) (string, error) {
	if commit != "" {
		return value(ctx, store, "rev-parse", commit+"^{tree}")
	}
	b, e := git(ctx, store, nil, nil, "mktree")
	return strings.TrimSpace(string(b)), e
}

func rightTree(ctx context.Context, store, head string, entries []entry) (string, error) {
	if entries == nil {
		return commitTree(ctx, store, head)
	}
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(store, "qual-index")}
	if _, e := git(ctx, store, nil, env, "read-tree", "--empty"); e != nil {
		return "", e
	}
	var records bytes.Buffer
	for _, item := range entries {
		oid := item.oid
		if oid == "" {
			b, e := git(ctx, store, item.data, nil, "hash-object", "-w", "--stdin")
			if e != nil {
				return "", e
			}
			oid = strings.TrimSpace(string(b))
		}
		fmt.Fprintf(&records, "%s %s\t%s%c", item.mode, oid, item.path, 0)
	}
	if _, e := git(ctx, store, records.Bytes(), env, "update-index", "-z", "--index-info"); e != nil {
		return "", e
	}
	b, e := git(ctx, store, nil, env, "write-tree")
	return strings.TrimSpace(string(b)), e
}

func projectIdentity(ctx context.Context, root string) (string, error) {
	identity, e := value(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return "", e
	}
	// Query all remotes: an absent remote is normal and not a failed subprocess.
	b, e := git(ctx, root, nil, nil, "config", "--list", "--null")
	if e != nil {
		return "", e
	}
	for _, record := range bytes.Split(b, []byte{0}) {
		key, val, ok := strings.Cut(string(record), "\n")
		if ok && key == "remote.origin.url" {
			return val, nil
		}
	}
	return identity, nil
}

func submodulePaths(ctx context.Context, store, base, head string) (map[string]bool, error) {
	var b []byte
	var e error
	gitlinks := map[string]bool{}
	for _, tree := range []string{base, head} {
		b, e = git(ctx, store, nil, nil, "ls-tree", "-r", "-z", tree)
		if e != nil {
			return nil, e
		}
		for _, line := range bytes.Split(b, []byte{0}) {
			left, path, ok := strings.Cut(string(line), "\t")
			if ok && strings.HasPrefix(left, "160000 ") {
				gitlinks[path] = true
			}
		}
	}
	return gitlinks, nil
}

func (s *Source) storageHome(req domain.SourceRequest, root string) (string, error) {
	home := s.home
	if req.Home != "" {
		home = req.Home
	}
	if home == "" || !validID(string(req.RunID)) {
		return "", failure("invalid_config", "home and safe run ID required", nil)
	}
	home, e := canonicalDestination(home)
	if e != nil {
		return "", e
	}
	relative, e := filepath.Rel(root, home)
	if e != nil {
		return "", e
	}
	if relative == "." || (!strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "..") {
		return "", failure("invalid_config", "QUAL_HOME must be outside source", nil)
	}
	return home, nil
}

// Resolve existing ancestors before creating storage: a symlink outside the
// source must not redirect QUAL_HOME back into the source checkout.
func canonicalDestination(path string) (string, error) {
	absolute, e := filepath.Abs(path)
	if e != nil {
		return "", e
	}
	resolved, e := filepath.EvalSymlinks(absolute)
	if e == nil {
		return resolved, nil
	}
	if !os.IsNotExist(e) {
		return "", e
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return "", e
	}
	resolved, e = canonicalDestination(parent)
	if e != nil {
		return "", e
	}
	return filepath.Join(resolved, filepath.Base(absolute)), nil
}

func snapshotKind(req domain.SourceRequest, entries []entry) string {
	if entries == nil {
		return "commits"
	}
	if req.Staged {
		return "staged"
	}
	return "worktree"
}

func sourceError(err error) error {
	if err != nil && domain.CodeOf(err) == "" {
		return failure("source_unavailable", "Git source operation failed", err)
	}
	return err
}
