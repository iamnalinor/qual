package gitstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := gitTestCommand(t, dir, args...)
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s: %v", args, b, e)
	}
	return string(b)
}

func gitTestCommand(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	// #nosec G204 -- fixed Git executable and controlled integration-test arguments.
	c := exec.CommandContext(context.Background(), "git", args...)
	c.Dir = dir
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "GIT_") {
			c.Env = append(c.Env, variable)
		}
	}
	c.Env = append(c.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	return c
}

func TestSelectorsUntrackedSymlinksAndUnborn(t *testing.T) {
	ctx := context.Background()
	r := t.TempDir()
	gitTest(t, r, "init", "-q")
	writeTest(t, r, "tracked", "first")
	gitTest(t, r, "add", "tracked")
	writeTest(t, r, "tracked", "working")
	writeTest(t, r, "untracked", "new")
	writeTest(t, r, ".gitignore", "ignored\n")
	writeTest(t, r, "ignored", "secret")
	if e := os.Symlink("/outside/missing", filepath.Join(r, "link")); e != nil {
		t.Fatal(e)
	}
	s := New(t.TempDir())
	snap, e := s.Prepare(ctx, domain.SourceRequest{Target: r, RunID: "unborn", IncludeUntracked: true})
	if e != nil {
		t.Fatal(e)
	}
	w, e := s.Worktree(ctx, snap, "reviewer")
	if e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"tracked", "untracked", ".gitignore"} {
		if _, e = os.Stat(filepath.Join(w, name)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = os.Stat(filepath.Join(w, "ignored")); !os.IsNotExist(e) {
		t.Fatalf("ignored file included: %v", e)
	}
	link, e := os.Readlink(filepath.Join(w, "link"))
	if e != nil || link != "/outside/missing" {
		t.Fatalf("symlink %q %v", link, e)
	}
	for _, req := range []domain.SourceRequest{{Staged: true, Worktree: true}, {Head: "HEAD"}, {Staged: true, IncludeUntracked: true}, {Base: "HEAD", IncludeUntracked: true}} {
		req.Target = r
		req.RunID = "invalid"
		_, e = s.Prepare(ctx, req)
		if domain.CodeOf(e) != "invalid_config" {
			t.Fatalf("got %v", e)
		}
	}
}

func TestDirectCommitComparisonSubdirectoryAndStale(t *testing.T) {
	ctx := context.Background()
	r := repoTest(t)
	first := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD"))
	gitTest(t, r, "checkout", "-qb", "side")
	writeTest(t, r, "file", "side")
	gitTest(t, r, "commit", "-qam", "side")
	side := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD"))
	gitTest(t, r, "checkout", "--detach", first)
	writeTest(t, r, "other", "other")
	gitTest(t, r, "add", ".")
	gitTest(t, r, "commit", "-qm", "other")
	base := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD"))
	if e := os.Mkdir(filepath.Join(r, "sub"), 0o700); e != nil {
		t.Fatal(e)
	}
	s := New(t.TempDir())
	snap, e := s.Prepare(ctx, domain.SourceRequest{Target: filepath.Join(r, "sub"), Base: base, Head: side, RunID: "commits"})
	if e != nil {
		t.Fatal(e)
	}
	if snap.BaseCommit != base || snap.Root != r || len(snap.Files) != 2 {
		t.Fatalf("unexpected snapshot %+v", snap)
	}
	w, e := s.Worktree(ctx, snap, "reviewer")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(w, "other")); !os.IsNotExist(e) {
		t.Fatal("direct comparison failed")
	}
}

func TestSelectedWorkingChangesMakeSnapshotStale(t *testing.T) {
	ctx := context.Background()
	r := repoTest(t)
	s := New(t.TempDir())
	var e error
	writeTest(t, r, "file", "dirty")
	dirty, e := s.Prepare(ctx, domain.SourceRequest{Target: r, RunID: "dirty"})
	if e != nil {
		t.Fatal(e)
	}
	stale, e := s.Stale(ctx, dirty)
	if e != nil || stale {
		t.Fatalf("new snapshot stale: %v %v", stale, e)
	}
	writeTest(t, r, "file", "changed")
	stale, e = s.Stale(ctx, dirty)
	if e != nil || !stale {
		t.Fatalf("changed snapshot: %v %v", stale, e)
	}
}

func TestRetentionPreservesActivePinnedAndExpiresCompleted(t *testing.T) {
	ctx := context.Background()
	r := repoTest(t)
	writeTest(t, r, "file", "dirty")
	s := New(t.TempDir())
	snap, e := s.Prepare(ctx, domain.SourceRequest{Target: r, RunID: "retention"})
	if e != nil {
		t.Fatal(e)
	}
	for _, agent := range []domain.AgentID{"active", "pinned", "expired"} {
		if _, e = s.Worktree(ctx, snap, agent); e != nil {
			t.Fatal(e)
		}
	}
	done := time.Now().Add(-8 * 24 * time.Hour)
	if e = s.Retain(ctx, snap.RunID, "pinned", &done, true); e != nil {
		t.Fatal(e)
	}
	if e = s.Retain(ctx, snap.RunID, "expired", &done, false); e != nil {
		t.Fatal(e)
	}
	removed, e := s.Cleanup(ctx, time.Now(), 0)
	if e != nil || len(removed) != 1 {
		t.Fatalf("cleanup %v %v", removed, e)
	}
	for _, agent := range []string{"active", "pinned"} {
		if _, e = os.Stat(filepath.Join(s.home, "worktrees", string(snap.RunID), agent)); e != nil {
			t.Fatal(e)
		}
	}
}

func TestNoChangesAndUnmergedIndex(t *testing.T) {
	// The merge fixture must not depend on a developer's configured identity.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	ctx := context.Background()
	r := repoTest(t)
	s := New(t.TempDir())
	_, e := s.Prepare(ctx, domain.SourceRequest{Target: r, RunID: "clean"})
	if domain.CodeOf(e) != "no_changes" {
		t.Fatalf("got %v", e)
	}
	gitTest(t, r, "checkout", "-qb", "conflict")
	writeTest(t, r, "file", "side\n")
	gitTest(t, r, "commit", "-qam", "side")
	gitTest(t, r, "checkout", "--detach", "HEAD~1")
	writeTest(t, r, "file", "main\n")
	gitTest(t, r, "commit", "-qam", "main")
	cmd := gitTestCommand(t, r, "merge", "conflict")
	output, mergeErr := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(mergeErr, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("expected merge conflict exit 1, got %v: %s", mergeErr, output)
	}
	if entries := gitTest(t, r, "ls-files", "--unmerged"); entries == "" {
		t.Fatalf("merge did not create unmerged index entries: %s", output)
	}
	_, e = s.Prepare(ctx, domain.SourceRequest{Target: r, Staged: true, RunID: "conflict"})
	if domain.CodeOf(e) != "unmerged_index" {
		t.Fatalf("got %v", e)
	}
}

func TestLinkedWorktreeCapturesBinaryAndGitlinksWithoutFilters(t *testing.T) {
	ctx := context.Background()
	r := repoTest(t)
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, r, "worktree", "add", "--detach", linked, "HEAD")
	writeTest(t, linked, "file", "working\n")
	writeTest(t, linked, "binary", string([]byte{0, 1, 2}))
	writeTest(t, linked, ".gitattributes", "file filter=unsafe\n")
	gitTest(t, linked, "add", "binary", ".gitattributes")
	head := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD"))
	gitTest(t, linked, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
	marker := filepath.Join(t.TempDir(), "called")
	gitTest(t, r, "config", "filter.unsafe.clean", "touch "+marker)
	gitTest(t, r, "config", "filter.unsafe.smudge", "touch "+marker)
	gitTest(t, r, "config", "core.hooksPath", filepath.Dir(marker))
	installExecutionGuards(t, r, marker)

	before := strings.TrimSpace(gitTest(t, r, "count-objects", "-v"))
	s := New(t.TempDir())
	snap, e := s.Prepare(ctx, domain.SourceRequest{Target: linked, RunID: "linked"})
	if e != nil {
		t.Fatal(e)
	}
	w, e := s.Worktree(ctx, snap, "reviewer")
	if e != nil {
		t.Fatal(e)
	}
	data := readTest(t, filepath.Join(w, "file"))
	if string(data) != "working\n" {
		t.Fatalf("got %q", data)
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("filter executed")
	}
	if before != strings.TrimSpace(gitTest(t, r, "count-objects", "-v")) {
		t.Fatal("source object store changed")
	}
	binary, gitlink := false, false
	for _, f := range snap.Files {
		if f.Path == "binary" {
			binary = f.Binary
		}
		if f.Path == "module" {
			gitlink = f.ExcludedReason == "submodule_not_expanded"
		}
	}
	if !binary || !gitlink {
		t.Fatalf("missing binary/submodule manifest: %+v", snap.Files)
	}
}

func TestImportRejectsRefsAndKeepsIndependentObjects(t *testing.T) {
	ctx := context.Background()
	r := repoTest(t)
	base := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD"))
	writeTest(t, r, "file", "new")
	gitTest(t, r, "commit", "-qam", "new")
	head := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD"))
	s := New(t.TempDir())
	if _, e := s.Import(ctx, domain.SourceRequest{RunID: "invalid"}, r, base, "HEAD"); domain.CodeOf(e) != "invalid_config" {
		t.Fatalf("got %v", e)
	}
	snap, e := s.Import(ctx, domain.SourceRequest{RunID: "remote"}, r, base, head)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.RemoveAll(r); e != nil {
		t.Fatal(e)
	}
	w, e := s.Worktree(ctx, snap, "leader")
	if e != nil {
		t.Fatal(e)
	}
	data := readTest(t, filepath.Join(w, "file"))
	if string(data) != "new" {
		t.Fatalf("independent checkout got %q", data)
	}
}

func TestTrackedParentSymlinkCannotReadOutsideRepository(t *testing.T) {
	r := repoTest(t)
	if e := os.Mkdir(filepath.Join(r, "dir"), 0o700); e != nil {
		t.Fatal(e)
	}
	writeTest(t, r, "dir/file", "inside")
	gitTest(t, r, "add", ".")
	gitTest(t, r, "commit", "-qm", "directory")
	if e := os.RemoveAll(filepath.Join(r, "dir")); e != nil {
		t.Fatal(e)
	}
	outside := t.TempDir()
	writeTest(t, outside, "file", "outside data")
	if e := os.Symlink(outside, filepath.Join(r, "dir")); e != nil {
		t.Fatal(e)
	}
	_, e := New(t.TempDir()).Prepare(context.Background(), domain.SourceRequest{Target: r, RunID: "escape"})
	if domain.CodeOf(e) != "source_unavailable" {
		t.Fatalf("expected confined read failure, got %v", e)
	}
}

func TestCancelledCaptureFailsBeforeWorktreeCreation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e := New(t.TempDir()).Prepare(ctx, domain.SourceRequest{Target: repoTest(t), RunID: "cancelled"})
	if e == nil {
		t.Fatal("cancelled Prepare succeeded")
	}
}

func writeTest(t *testing.T, root, name, content string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); e != nil {
		t.Fatal(e)
	}
}

func repoTest(t *testing.T) string {
	t.Helper()
	r := t.TempDir()
	gitTest(t, r, "init", "-q")
	writeTest(t, r, "file", "base\n")
	gitTest(t, r, "add", ".")
	gitTest(t, r, "commit", "-qm", "base")
	return r
}

func TestPartialStagingAndImmutableRoleTrees(t *testing.T) {
	r := repoTest(t)
	writeTest(t, r, "file", "staged\n")
	gitTest(t, r, "add", "file")
	writeTest(t, r, "file", "working\n")
	before := gitTest(t, r, "status", "--porcelain=v1")
	index := readTest(t, filepath.Join(r, ".git", "index"))
	refs := gitTest(t, r, "show-ref")
	s := New(t.TempDir())
	ctx := context.Background()
	for _, staged := range []bool{true, false} {
		snap, e := s.Prepare(ctx, domain.SourceRequest{Target: r, Staged: staged, RunID: domain.RunID(map[bool]string{true: "staged", false: "working"}[staged])})
		if e != nil {
			t.Fatal(e)
		}
		a, e := s.Worktree(ctx, snap, "reviewer")
		if e != nil {
			t.Fatal(e)
		}
		want := "working\n"
		if staged {
			want = "staged\n"
		}
		b := readTest(t, filepath.Join(a, "file"))
		if string(b) != want {
			t.Fatalf("got %q want %q", b, want)
		}
		writeTest(t, a, "file", "experiment")
		leader, e := s.Worktree(ctx, snap, "leader")
		if e != nil {
			t.Fatal(e)
		}
		b = readTest(t, filepath.Join(leader, "file"))
		if string(b) != want {
			t.Fatalf("leader got %q", b)
		}
	}
	afterIndex := readTest(t, filepath.Join(r, ".git", "index"))
	if string(index) != string(afterIndex) || refs != gitTest(t, r, "show-ref") || before != gitTest(t, r, "status", "--porcelain=v1") {
		t.Fatal("source changed")
	}
}

func readTest(t *testing.T, path string) []byte {
	t.Helper()
	// #nosec G304 -- paths are created and controlled by these real-repository tests.
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func installExecutionGuards(t *testing.T, r, marker string) {
	t.Helper()
	monitor := filepath.Join(filepath.Dir(marker), "monitor")
	writeTest(t, filepath.Dir(marker), "monitor", "#!/bin/sh\ntouch "+marker+"\n")
	// #nosec G302 -- executable script intentionally tests that Git never executes configured commands.
	if e := os.Chmod(monitor, 0o700); e != nil {
		t.Fatal(e)
	}
	gitTest(t, r, "config", "core.fsmonitor", monitor)
	writeTest(t, filepath.Dir(marker), "post-checkout", "#!/bin/sh\ntouch "+marker+"\n")
	// #nosec G302 -- hook must be executable to detect an accidental hook invocation.
	if e := os.Chmod(filepath.Join(filepath.Dir(marker), "post-checkout"), 0o700); e != nil {
		t.Fatal(e)
	}
}

func TestConcurrentSelectedChangesStopAfterThreeCaptures(t *testing.T) {
	r := repoTest(t)
	s := New(t.TempDir())
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for sequence := 0; ; sequence++ {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			temporary := filepath.Join(r, "replacement")
			if e := os.WriteFile(temporary, []byte(fmt.Sprint(sequence)), 0o600); e != nil {
				done <- e
				return
			}
			if e := os.Rename(temporary, filepath.Join(r, "file")); e != nil {
				done <- e
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	_, err := s.Prepare(context.Background(), domain.SourceRequest{Target: r, RunID: "changing"})
	close(stop)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if domain.CodeOf(err) != "source_changed" {
		t.Fatalf("got %v", err)
	}
	directories, e := filepath.Glob(filepath.Join(s.home, "snapshots", "changing", "repo-*"))
	if e != nil || len(directories) != 0 {
		t.Fatalf("abandoned snapshots: %v %v", directories, e)
	}
}

func TestHomeSymlinkCannotPlaceStoreInsideSource(t *testing.T) {
	r := repoTest(t)
	writeTest(t, r, "file", "dirty")
	parent := t.TempDir()
	if e := os.Symlink(r, filepath.Join(parent, "alias")); e != nil {
		t.Fatal(e)
	}
	_, e := New(filepath.Join(parent, "alias", "qual")).Prepare(context.Background(), domain.SourceRequest{Target: r, RunID: "unsafe-home"})
	if domain.CodeOf(e) != "invalid_config" {
		t.Fatalf("got %v", e)
	}
	if _, e = os.Stat(filepath.Join(r, "qual")); !os.IsNotExist(e) {
		t.Fatal("source directory was modified")
	}
}

func TestCleanupKeepsRegistryWhenGitRemovalFails(t *testing.T) {
	ctx := context.Background()
	r := repoTest(t)
	writeTest(t, r, "file", "dirty")
	s := New(t.TempDir())
	snap, e := s.Prepare(ctx, domain.SourceRequest{Target: r, RunID: "failed-cleanup"})
	if e != nil {
		t.Fatal(e)
	}
	work, e := s.Worktree(ctx, snap, "reviewer")
	if e != nil {
		t.Fatal(e)
	}
	done := time.Now().Add(-8 * 24 * time.Hour)
	if e = s.Retain(ctx, snap.RunID, "reviewer", &done, false); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(snap.StorePath, snap.StorePath+"-moved"); e != nil {
		t.Fatal(e)
	}
	registryBefore := string(readTest(t, work+".json"))
	if _, e = s.Cleanup(ctx, time.Now(), 0); e == nil {
		t.Fatal("expected removal failure")
	}
	if _, e = os.Stat(work + ".json"); e != nil {
		t.Fatalf("failed cleanup lost registry: %v", e)
	}
	if _, e = os.Stat(work); e != nil {
		t.Fatalf("failed cleanup lost worktree: %v", e)
	}
	if after := string(readTest(t, work+".json")); after != registryBefore {
		t.Fatal("failed cleanup changed registry")
	}
}
