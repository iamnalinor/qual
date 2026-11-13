package gitstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestWorkingFileDirectoryReplacements(t *testing.T) {
	for _, directoryFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "file_to_directory", true: "directory_to_file"}[directoryFirst], func(t *testing.T) {
			r, replacement := replacementFixture(t, directoryFirst)
			s := New(t.TempDir())
			assertReplacementExcluded(t, s, r)
			snap, e := s.Prepare(context.Background(), domain.SourceRequest{Target: r, RunID: "replacement", IncludeUntracked: true})
			if e != nil {
				t.Fatal(e)
			}
			w, e := s.Worktree(context.Background(), snap, "reviewer")
			if e != nil {
				t.Fatal(e)
			}
			if got := string(readTest(t, filepath.Join(w, replacement))); got != "replacement" {
				t.Fatalf("replacement %q", got)
			}
		})
	}
}

func TestUnsuccessfulSnapshotsDoNotLeakStores(t *testing.T) {
	ctx := context.Background()
	r := repoTest(t)
	head := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD"))
	s := New(t.TempDir())
	t.Run("working_no_changes", func(t *testing.T) {
		_, e := s.Prepare(ctx, domain.SourceRequest{Target: r, RunID: "clean"})
		if domain.CodeOf(e) != "no_changes" {
			t.Fatalf("got %v", e)
		}
		noStores(t, s.home, "clean")
		noAllocation(t, s.home, "clean")
	})
	t.Run("commit_no_changes", func(t *testing.T) {
		_, e := s.Import(ctx, domain.SourceRequest{RunID: "equal"}, r, head, head)
		if domain.CodeOf(e) != "no_changes" {
			t.Fatalf("got %v", e)
		}
		noStores(t, s.home, "equal")
		noAllocation(t, s.home, "equal")
	})
	t.Run("missing_index_object", func(t *testing.T) {
		_, e := s.build(ctx, domain.SourceRequest{RunID: "missing"}, r, head, head, []entry{{path: "file", mode: "100644", oid: strings.Repeat("1", 40)}})
		if e == nil {
			t.Fatal("missing object accepted")
		}
		noStores(t, s.home, "missing")
	})
	t.Run("cancelled_build", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, e := s.build(cancelled, domain.SourceRequest{RunID: "cancelled"}, r, head, head, nil)
		if e == nil {
			t.Fatal("cancelled build accepted")
		}
		noStores(t, s.home, "cancelled")
	})
}

func noStores(t *testing.T, home, run string) {
	t.Helper()
	paths, e := filepath.Glob(filepath.Join(home, "snapshots", run, "repo-*"))
	if e != nil {
		t.Fatal(e)
	}
	if len(paths) != 0 {
		t.Fatalf("leaked stores: %v", paths)
	}
}

func TestCaptureVerificationErrorsRemoveBuiltStore(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "source_disappeared", true: "cancelled"}[cancelled], func(t *testing.T) {
			verifyCaptureFailure(t, cancelled)
		})
	}
}

func TestImportDoesNotCopyUnselectedHistory(t *testing.T) {
	r := repoTest(t)
	ancestor := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD"))
	writeTest(t, r, "historical", "deleted historical content")
	gitTest(t, r, "add", ".")
	gitTest(t, r, "commit", "-qm", "historical blob")
	historical := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD:historical"))
	gitTest(t, r, "rm", "historical")
	gitTest(t, r, "commit", "-qm", "base")
	base := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD"))
	writeTest(t, r, "file", "head")
	gitTest(t, r, "commit", "-qam", "head")
	head := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD"))
	s := New(t.TempDir())
	snap, e := s.Import(context.Background(), domain.SourceRequest{RunID: "bounded"}, r, base, head)
	if e != nil {
		t.Fatal(e)
	}
	for _, oid := range []string{ancestor, historical} {
		if _, e = git(context.Background(), snap.StorePath, nil, nil, "cat-file", "-e", oid); e == nil {
			t.Fatalf("unselected history object %s was imported", oid)
		}
	}
	gitTest(t, snap.StorePath, "fsck", "--full")
	if e = os.RemoveAll(r); e != nil {
		t.Fatal(e)
	}
	w, e := s.Worktree(context.Background(), snap, "leader")
	if e != nil {
		t.Fatal(e)
	}
	if got := string(readTest(t, filepath.Join(w, "file"))); got != "head" {
		t.Fatalf("got %q", got)
	}
}

func TestAllowEmptyCreatesUsableSetupSnapshots(t *testing.T) {
	for _, staged := range []bool{false, true} {
		t.Run(map[bool]string{false: "working", true: "staged"}[staged], func(t *testing.T) {
			r := repoTest(t)
			s := New(t.TempDir())
			snap, e := s.Prepare(context.Background(), domain.SourceRequest{Target: r, RunID: "setup", Staged: staged, AllowEmpty: true})
			if e != nil {
				t.Fatal(e)
			}
			if len(snap.Files) != 0 {
				t.Fatalf("unexpected diff %+v", snap.Files)
			}
			work, e := s.Worktree(context.Background(), snap, "setup")
			if e != nil {
				t.Fatal(e)
			}
			if got := string(readTest(t, filepath.Join(work, "file"))); got != "base\n" {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestCancellationAfterAllocationRemovesAttempt(t *testing.T) {
	r := repoTest(t)
	writeTest(t, r, "file", "dirty")
	s := New(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, e := s.Prepare(ctx, domain.SourceRequest{Target: r, RunID: "cancel-after-allocation"})
		done <- e
	}()
	for {
		paths, e := filepath.Glob(filepath.Join(s.home, "snapshots", "cancel-after-allocation", "repo-*"))
		if e != nil {
			t.Fatal(e)
		}
		if len(paths) > 0 {
			cancel()
			break
		}
		select {
		case e = <-done:
			t.Fatalf("Prepare completed before cancellation: %v", e)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if e := <-done; e == nil {
		t.Fatal("cancelled preparation succeeded")
	}
	noStores(t, s.home, "cancel-after-allocation")
}

func replacementFixture(t *testing.T, directoryFirst bool) (string, string) {
	t.Helper()
	r := repoTest(t)
	if directoryFirst {
		if e := os.Mkdir(filepath.Join(r, "path"), 0o700); e != nil {
			t.Fatal(e)
		}
		writeTest(t, r, "path/old", "old")
	} else {
		writeTest(t, r, "path", "old")
	}
	gitTest(t, r, "add", ".")
	gitTest(t, r, "commit", "-qm", "before refactor")
	if e := os.RemoveAll(filepath.Join(r, "path")); e != nil {
		t.Fatal(e)
	}
	replacement := "path"
	if !directoryFirst {
		replacement = "path/new"
		if e := os.Mkdir(filepath.Join(r, "path"), 0o700); e != nil {
			t.Fatal(e)
		}
	}
	writeTest(t, r, replacement, "replacement")

	return r, replacement
}

func verifyCaptureFailure(t *testing.T, cancelled bool) {
	t.Helper()
	ctx := context.Background()
	r := repoTest(t)
	writeTest(t, r, "file", "dirty")
	head := strings.TrimSpace(gitTest(t, r, "rev-parse", "HEAD"))
	s := New(t.TempDir())
	req := domain.SourceRequest{RunID: "verify"}
	first, e := capture(ctx, r, false, false)
	if e != nil {
		t.Fatal(e)
	}
	snap, e := s.build(ctx, req, r, head, head, first)
	if e != nil {
		t.Fatal(e)
	}
	if cancelled {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		cancel()
	} else {
		if e = os.RemoveAll(r); e != nil {
			t.Fatal(e)
		}
	}
	if _, _, e = finishCapture(ctx, req, snap, r, head, first); e == nil {
		t.Fatal("expected verification error")
	}
	noStores(t, s.home, "verify")
}

func noAllocation(t *testing.T, home, run string) {
	t.Helper()
	if _, e := os.Stat(filepath.Join(home, "snapshots", run)); !os.IsNotExist(e) {
		t.Fatalf("clean comparison allocated storage: %v", e)
	}
}

func assertReplacementExcluded(t *testing.T, s *Source, r string) {
	t.Helper()
	ctx := context.Background()
	snap, e := s.Prepare(ctx, domain.SourceRequest{Target: r, RunID: "tracked-only"})
	if e != nil {
		t.Fatal(e)
	}
	w, e := s.Worktree(ctx, snap, "reviewer")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(w, "path")); !os.IsNotExist(e) {
		t.Fatalf("untracked replacement included: %v", e)
	}
}
