package platform

import (
	"context"
	"encoding/pem"
	"net/http"

	// #nosec G504 -- Go 1.27 test-only localhost CGI fixture; historical pre-1.6.3 advisory does not apply.
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestHTTPSFetchDeepensExactCapturedRevisions(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo.git")
	checkError(t, os.Mkdir(repo, 0o700))
	command(t, repo, "init")
	base := commit(t, repo, "base")
	for i := 0; i < 40; i++ {
		commit(t, repo, "head"+strconv.Itoa(i))
	}
	head := command(t, repo, "rev-parse", "HEAD")
	command(t, repo, "checkout", "--detach", base)
	for i := 0; i < 40; i++ {
		commit(t, repo, "target"+strconv.Itoa(i))
	}
	target := command(t, repo, "rev-parse", "HEAD")
	command(t, repo, "update-ref", "refs/heads/source", head)
	command(t, repo, "update-ref", "refs/heads/target", target)
	gitPath, err := exec.LookPath("git")
	checkError(t, err)
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { backend.ServeHTTP(w, r) }))
	defer server.Close()
	certificate := filepath.Join(t.TempDir(), "ca.pem")
	if err = os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(server.URL)
	checkError(t, err)
	metadata := ReviewContext{Target: Target{Host: parsed.Host, Kind: "gitlab"}, Head: head, TargetSHA: target, HeadURL: server.URL + "/repo.git", TargetURL: server.URL + "/repo.git"}
	source := New(Config{Home: t.TempDir(), GitCAFile: certificate})
	fetched, cleanup, err := source.repository(context.Background(), metadata)
	checkError(t, err)
	defer func() { checkError(t, cleanup()) }()
	if _, err = runGit(context.Background(), fetched, nil, "merge-base", "--all", head, target); err == nil {
		t.Fatal("initial history unexpectedly reached base")
	}
	got, err := source.mergeBase(context.Background(), fetched, metadata)
	if err != nil || got != base {
		t.Fatalf("deepened base %s %v", got, err)
	}
	if command(t, fetched, "rev-parse", head) != head {
		t.Fatal("head changed")
	}
}

func TestRejectsMultipleMergeBases(t *testing.T) {
	repo := t.TempDir()
	command(t, repo, "init")
	base := commit(t, repo, "base")
	a := commit(t, repo, "a")
	command(t, repo, "checkout", "--detach", base)
	b := commit(t, repo, "b")
	tree := command(t, repo, "rev-parse", b+"^{tree}")
	left := command(t, repo, "commit-tree", tree, "-p", a, "-p", b, "-m", "left")
	right := command(t, repo, "commit-tree", tree, "-p", b, "-p", a, "-m", "right")
	source := New(Config{})
	if _, err := source.mergeBase(context.Background(), repo, ReviewContext{Head: left, TargetSHA: right}); err == nil {
		t.Fatal("accepted ambiguous bases")
	}
}

func TestShallowVisibleBaseCannotHideSecondBase(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo.git")
	checkError(t, os.Mkdir(repo, 0o700))
	command(t, repo, "init")
	base := commit(t, repo, "root")
	tree := command(t, repo, "rev-parse", base+"^{tree}")
	a := command(t, repo, "commit-tree", tree, "-p", base, "-m", "a")
	b := command(t, repo, "commit-tree", tree, "-p", base, "-m", "b")
	left, right := b, b
	for i := 0; i < 40; i++ {
		left = command(t, repo, "commit-tree", tree, "-p", left, "-m", "left"+strconv.Itoa(i))
		right = command(t, repo, "commit-tree", tree, "-p", right, "-m", "right"+strconv.Itoa(i))
	}
	head := command(t, repo, "commit-tree", tree, "-p", a, "-p", left, "-m", "head")
	target := command(t, repo, "commit-tree", tree, "-p", a, "-p", right, "-m", "target")
	command(t, repo, "update-ref", "refs/heads/head", head)
	command(t, repo, "update-ref", "refs/heads/target", target)
	endpoint, certificate := serveGit(t, root)
	parsed, err := url.Parse(endpoint)
	checkError(t, err)
	metadata := ReviewContext{Target: Target{Host: parsed.Host, Kind: "gitlab"}, Head: head, TargetSHA: target, HeadURL: endpoint + "/repo.git", TargetURL: endpoint + "/repo.git"}
	source := New(Config{Home: t.TempDir(), GitCAFile: certificate})
	fetched, cleanup, err := source.repository(context.Background(), metadata)
	checkError(t, err)
	defer func() { checkError(t, cleanup()) }()
	if _, err = source.mergeBase(context.Background(), fetched, metadata); err == nil {
		t.Fatal("shallow history hid second merge base")
	}
}

func serveGit(t *testing.T, root string) (string, string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	checkError(t, err)
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewTLSServer(backend)
	t.Cleanup(server.Close)
	certificate := filepath.Join(t.TempDir(), "ca.pem")
	checkError(t, os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600))
	return server.URL, certificate
}

func TestIncompleteBaseCheckoutFallsBackToHTTPS(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "repo.git")
	checkError(t, os.Mkdir(remote, 0o700))
	command(t, remote, "init")
	base := commit(t, remote, "base-only-content")
	head := commit(t, remote, "head-content")
	command(t, remote, "update-ref", "refs/heads/source", head)
	command(t, remote, "checkout", "--detach", base)
	target := commit(t, remote, "target-content")
	checkout := filepath.Join(t.TempDir(), "checkout")
	command(t, root, "clone", "--local", "--no-hardlinks", remote, checkout)
	blob := command(t, checkout, "rev-parse", base+":rules")
	checkError(t, os.Remove(filepath.Join(checkout, ".git", "objects", blob[:2], blob[2:])))
	endpoint, certificate := serveGit(t, root)
	parsed, err := url.Parse(endpoint)
	checkError(t, err)
	metadata := ReviewContext{Target: Target{Host: parsed.Host, Kind: "gitlab", Repository: "repo", Number: "1"}, Head: head, TargetSHA: target, HeadURL: endpoint + "/repo.git", TargetURL: endpoint + "/repo.git"}
	assertCompleteEndpointsMissingBase(t, checkout, base, metadata)
	source := New(Config{Home: t.TempDir(), Checkout: checkout, GitCAFile: certificate})
	repository, cleanup, err := source.repository(t.Context(), metadata)
	checkError(t, err)
	defer func() { checkError(t, cleanup()) }()
	if repository == checkout {
		t.Fatal("reused checkout with missing base-only blob")
	}
	actualBase, err := source.mergeBase(t.Context(), repository, metadata)
	checkError(t, err)
	assertString(t, actualBase, base)
	snapshot, err := source.importSnapshot(t.Context(), domain.SourceRequest{RunID: "incomplete-base"}, repository, actualBase, metadata)
	checkError(t, err)
	assertString(t, command(t, snapshot.StorePath, "show", base+":rules"), "base-only-content")
	assertString(t, command(t, snapshot.StorePath, "show", head+":rules"), "head-content")
	assertString(t, command(t, snapshot.StorePath, "show", target+":rules"), "target-content")
	// The original checkout remains incomplete: fallback must not fetch into it.
	if _, err = runGit(t.Context(), checkout, nil, "cat-file", "-e", blob); err == nil {
		t.Fatal("fallback changed original checkout")
	}
}

func assertCompleteEndpointsMissingBase(t *testing.T, checkout, base string, metadata ReviewContext) {
	t.Helper()
	if !exactObjects(t.Context(), checkout, metadata) {
		t.Fatal("fixture endpoints must be complete and history nonshallow")
	}
	actualBase, err := runGit(t.Context(), checkout, nil, "merge-base", "--all", metadata.Head, metadata.TargetSHA)
	checkError(t, err)
	assertString(t, actualBase, base)
	if _, err = runGit(t.Context(), checkout, nil, "rev-list", "--objects", "--missing=error", "--no-walk", base, "--"); err == nil {
		t.Fatal("fixture base blob must be missing")
	}
}
