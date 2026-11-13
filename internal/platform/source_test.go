package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func command(t *testing.T, root string, args ...string) string {
	t.Helper()
	// #nosec G204 -- fixed git executable and test-owned arguments.
	c := exec.CommandContext(t.Context(), "git", args...)
	c.Dir = root
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			c.Env = append(c.Env, entry)
		}
	}
	c.Env = append(c.Env, "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s %v", args, b, e)
	}
	return strings.TrimSpace(string(b))
}

func commit(t *testing.T, root, content string) string {
	t.Helper()
	if e := os.WriteFile(filepath.Join(root, "rules"), []byte(content), 0o600); e != nil {
		t.Fatal(e)
	}
	command(t, root, "add", "rules")
	command(t, root, "commit", "-m", content)
	return command(t, root, "rev-parse", "HEAD")
}

func TestSnapshotForkStaleAndTargetRules(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("GIT_DIR", "/invalid/ambient/repository")
	repo := t.TempDir()
	command(t, repo, "init")
	base := commit(t, repo, "base")
	head := commit(t, repo, "head")
	command(t, repo, "checkout", "--detach", base)
	target := commit(t, repo, "target")
	current := head
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/org/repo/pulls/7":
			checkError(t, json.NewEncoder(w).Encode(map[string]any{"number": 7, "title": "A review", "head": map[string]any{"sha": current, "ref": "feature", "repo": map[string]any{"full_name": "fork/repo", "clone_url": "https://github.com/fork/repo.git"}}, "base": map[string]any{"sha": target, "ref": "main", "repo": map[string]any{"full_name": "org/repo", "clone_url": "https://github.com/org/repo.git"}}}))
		case "/repos/org/repo/commits/" + head + "/check-runs":
			writeResponse(t, w, []byte(`{"check_runs":[{"name":"test"}]}`))
		default:
			writeResponse(t, w, []byte(`[{"id":1}]`))
		}
	}))
	defer server.Close()
	source := New(Config{Home: t.TempDir(), HTTPClient: server.Client(), APIBaseURLs: map[string]string{"github.com": server.URL}, Checkout: repo})
	snap, err := source.Prepare(context.Background(), domain.SourceRequest{Target: "https://github.com/org/repo/pull/7", RunID: "run"})
	if err != nil {
		t.Fatal(err)
	}
	verifySnapshot(t, source, snap, repo, base, head, target)

	current = target
	stale, err := source.Stale(context.Background(), snap)
	if err != nil || !stale {
		t.Fatalf("stale %v %v", stale, err)
	}
	work, err := source.Worktree(context.Background(), snap, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- work is a test-owned temporary worktree.
	b, err := os.ReadFile(filepath.Join(work, "rules"))
	if err != nil || string(b) != "head" {
		t.Fatalf("worktree %s %v", b, err)
	}
}

func verifySnapshot(t *testing.T, source *Source, snap domain.Snapshot, repo, base, head, target string) {
	t.Helper()
	assertString(t, snap.BaseCommit, base)
	assertString(t, snap.OriginalHead, head)
	assertString(t, snap.TargetCommit, target)
	assertString(t, snap.RulesRevision, target)
	assertString(t, snap.Repository, "https://github.com/org/repo")
	metadata, err := source.Context(snap)
	if err != nil || metadata.HeadRepository != "fork/repo" || len(metadata.Items["files"]) != 1 {
		t.Fatalf("context %+v %v", metadata, err)
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if got := command(t, snap.StorePath, "show", target+":rules"); got != "target" {
		t.Fatalf("target rules: %s", got)
	}
	if got := command(t, snap.StorePath, "show", head+":rules"); got != "head" {
		t.Fatalf("head: %s", got)
	}
	if got := command(t, snap.StorePath, "show", base+":rules"); got != "base" {
		t.Fatalf("base: %s", got)
	}
}

func checkError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func writeResponse(t *testing.T, w io.Writer, data []byte) {
	t.Helper()
	if _, err := w.Write(data); err != nil {
		t.Error(err)
	}
}

func testTarget(t *testing.T, raw string) Target {
	t.Helper()
	target, err := ParseTarget(raw, nil)
	checkError(t, err)
	return target
}

func assertString(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
