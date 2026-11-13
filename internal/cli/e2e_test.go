package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func buildCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "qual")
	// #nosec G204 -- Executable and arguments are test-built fixtures; no user command execution.
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/qual")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", out, err)
	}
	return binary
}

func invoke(t *testing.T, binary, home, cwd, input string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// #nosec G204 -- Executable and arguments are test-built fixtures; no user command execution.
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "QUAL_HOME="+home, "QUAL_AUTH_BACKEND=file")
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func git(t *testing.T, repo string, args ...string) {
	t.Helper()
	// #nosec G204 -- Executable and arguments are test-built fixtures; no user command execution.
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git: %s: %v", out, err)
	}
}

func repository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "initial")
	return repo
}

func configure(t *testing.T, home, endpoint string) {
	t.Helper()
	data := "schema_version: 1\nconnections:\n  primary:\n    kind: openai\n    endpoint: " + endpoint + "\n    credential: env:QUAL_FIXTURE_API_KEY\nreviewers:\n  - id: reviewer\n    provider: primary\n    model: gpt-6-luna\nleader:\n  id: leader\n  provider: primary\n  model: gpt-6-luna\nsetup:\n  id: setup\n  provider: primary\n  model: gpt-6-luna\n"
	if err := os.WriteFile(filepath.Join(home, "settings.yaml"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExecutableNoChangesWithoutCredentials(t *testing.T) {
	binary := buildCLI(t)
	repo := repository(t)
	home := t.TempDir()
	configure(t, home, "http://127.0.0.1:1/v1")
	out, diagnostic, err := invoke(t, binary, home, repo, "", "review", "--staged", "--json", "--non-interactive")
	if err != nil {
		t.Fatalf("no changes: %v: %s", err, diagnostic)
	}
	if !strings.Contains(diagnostic, "not reported") {
		t.Fatalf("direct invocation misreported sandbox: %s", diagnostic)
	}
	var state struct {
		Status string `json:"status"`
		RunID  string `json:"run_id"`
	}
	if err = json.Unmarshal([]byte(out), &state); err != nil || state.Status != "no_changes" || state.RunID == "" {
		t.Fatalf("output %q: %v", out, err)
	}
	out, diagnostic, err = invoke(t, binary, home, repo, "", "sessions", "export", state.RunID)
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("export: %v %q %s", err, out, diagnostic)
	}
}

func TestExecutableAuthIsProtectedAndRedacted(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	cwd := t.TempDir()
	out, diagnostic, err := invoke(t, binary, home, cwd, "fixture-secret-not-for-output\n", "auth", "login", "--kind", "openai", "--profile", "fixture", "--stdin", "--json")
	if err != nil {
		t.Fatalf("login: %v: %s", err, diagnostic)
	}
	out2, diagnostic2, err := invoke(t, binary, home, cwd, "", "auth", "list", "--json")
	if err != nil || !strings.Contains(out2, "fixture") || strings.Contains(out+diagnostic+out2+diagnostic2, "fixture-secret-not-for-output") {
		t.Fatalf("unsafe auth output: %q %q %v", out2, diagnostic2, err)
	}
	info, err := os.Stat(filepath.Join(home, "auth", "fixture.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode: %v %v", info, err)
	}
}
