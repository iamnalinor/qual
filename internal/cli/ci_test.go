package cli_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iamnalinor/qual/internal/citemplate"
)

func TestExecutableCIAuthorizationHandoff(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	head := gitHead(t, repo)
	models, calls := fixture(t)
	defer models.Close()
	configure(t, home, models.URL+"/v1")
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	t.Setenv("GITHUB_TOKEN", "fixture-platform-token")
	var denied, draft atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-platform-token" {
			t.Error("platform credential missing")
		}
		var body string
		if strings.HasSuffix(r.URL.Path, "/permission") {
			permission := "write"
			if denied.Load() {
				permission = "read"
			}
			body = fmt.Sprintf(`{"permission":%q}`, permission)
		} else {
			body = fmt.Sprintf(`{"number":7,"state":"open","draft":%t,"html_url":"https://github.com/org/repo/pull/7","base":{"sha":%q,"repo":{"full_name":"org/repo"}},"head":{"sha":%q}}`, draft.Load(), head, head)
		}
		if _, err := fmt.Fprint(w, body); err != nil {
			t.Error(err)
		}
	}))
	defer api.Close()
	event := filepath.Join(home, "event.json")
	if err := os.WriteFile(event, []byte(`{"action":"opened","number":7,"repository":{"full_name":"org/repo"},"sender":{"login":"alice"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"ci", "authorize", "--provider", "github", "--event", "pull_request_target", "--repository", "org/repo", "--api-url", api.URL, "--event-file", event, "--trusted-source"}
	out, diagnostic, err := invoke(t, binary, home, repo, "", args...)
	if err != nil {
		t.Fatalf("authorize %v %s %s", err, out, diagnostic)
	}
	var decision citemplate.Decision
	if err = json.Unmarshal([]byte(out), &decision); err != nil {
		t.Fatal(err)
	}
	denied.Store(true)
	assertDenied(t, binary, home, repo, args)
	denied.Store(false)
	draft.Store(true)
	assertDenied(t, binary, home, repo, args)
	draft.Store(false)
	if calls.Load() != 0 {
		t.Fatal("authorization called model")
	}
	verifyCIReview(t, binary, home, repo, decision, calls)
}

func verifyCIReview(t *testing.T, binary, home, repo string, decision citemplate.Decision, calls *atomic.Int64) {
	t.Helper()
	var err error
	if err = os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed authorized fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, diagnostic, err := invoke(t, binary, home, repo, "", "review", "--worktree", "--expected-head", decision.HeadSHA, "--expected-target", decision.TargetSHA, "--json", "--non-interactive")
	if err != nil || !strings.Contains(out, `"status":"completed"`) {
		t.Fatalf("authorized review: %v %s %s", err, out, diagnostic)
	}
	before := calls.Load()
	git(t, repo, "add", "file.txt")
	git(t, repo, "commit", "-qm", "move source")
	out, diagnostic, err = invoke(t, binary, home, repo, "", "review", "--expected-head", decision.HeadSHA, "--expected-target", decision.TargetSHA, "--json", "--non-interactive")
	if err == nil || !strings.Contains(diagnostic, "ci_snapshot") || calls.Load() != before {
		t.Fatalf("moving SHA admitted: %v %s %s", err, out, diagnostic)
	}
}

func gitHead(t *testing.T, repo string) string {
	t.Helper()
	// #nosec G204 -- Fixed Git executable reads a test-owned commit ID.
	cmd := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", "HEAD")
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func assertDenied(t *testing.T, binary, home, repo string, args []string) {
	t.Helper()
	out, diagnostic, err := invoke(t, binary, home, repo, "", args...)
	if err == nil || out != "" {
		t.Fatalf("denied event produced approval: %v %s %s", err, out, diagnostic)
	}
}
