package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestExecutableFailuresAndCI(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	server, calls := fixture(t)
	defer server.Close()
	configure(t, home, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"review", "--json", "--non-interactive"}, {"review", "--expected-head", strings.Repeat("0", 40), "--json", "--non-interactive"}} {
		out, diagnostic, err := invoke(t, binary, home, repo, "", args...)
		if err == nil {
			t.Fatalf("expected failure %v: %s %s", args, out, diagnostic)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("missing auth or mismatched SHA called provider %d times", calls.Load())
	}
	out, diagnostic, err := invoke(t, binary, home, repo, "", "ci", "generate", "github")
	if err != nil || !strings.Contains(out, "pull_request_target:") || !strings.Contains(diagnostic, "QUAL_SOURCE_SHA") {
		t.Fatalf("CI generation: %v %s %s", err, out, diagnostic)
	}
	if err = os.WriteFile(filepath.Join(home, "settings.yaml"), []byte("unknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, diagnostic, err = invoke(t, binary, home, repo, "", "config", "validate", "--json")
	if err == nil || !strings.Contains(diagnostic, "invalid_config") {
		t.Fatalf("invalid config: %v %s %s", err, out, diagnostic)
	}
}

func TestExecutableInitConnectionFailureIsAtomic(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-key")
	_, diagnostic, err := invoke(t, binary, home, cwd, "", "init", "--model", "gpt-6-luna", "--endpoint", "http://127.0.0.1:1/v1", "--credential", "env:QUAL_FIXTURE_API_KEY", "--accept", "--non-interactive")
	if err == nil || !strings.Contains(diagnostic, "connect: connection refused") {
		t.Fatalf("init failure: %v %s", err, diagnostic)
	}
	if _, err = os.Stat(filepath.Join(home, "settings.yaml")); !os.IsNotExist(err) {
		t.Fatalf("failed init activated settings: %v", err)
	}
}

func TestExecutableNoChangesNeedsNoConfiguration(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	out, diagnostic, err := invoke(t, binary, home, repo, "", "review", "--json", "--non-interactive")
	if err != nil {
		t.Fatalf("empty config no changes: %v %s", err, diagnostic)
	}
	var state domain.RunState
	if err = json.Unmarshal([]byte(out), &state); err != nil || state.Status != domain.StatusNoChanges || state.Snapshot.RulesRevision == "" {
		t.Fatalf("wrong no_changes state: %s %v", out, err)
	}
}

func TestExecutableConfiguredKeyringNeverRelocatesCredentials(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	if err := os.WriteFile(filepath.Join(home, "auth-backend"), []byte("keyring\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A selected keyring remains selected even when a caller asks for file storage later.
	_, diagnostic, err := invoke(t, binary, home, cwd, "fixture-keyring-secret\n", "auth", "login", "--kind", "openai", "--profile", "fixture", "--stdin", "--json")
	if err == nil || !strings.Contains(diagnostic, "auth:") {
		t.Fatalf("unavailable configured keyring unexpectedly succeeded: %v %s", err, diagnostic)
	}
	if _, err = os.Stat(filepath.Join(home, "auth", "fixture.json")); !os.IsNotExist(err) {
		t.Fatalf("keyring failure relocated credentials: %v", err)
	}
	if strings.Contains(diagnostic, "fixture-keyring-secret") {
		t.Fatal("keyring error leaked secret")
	}
}

func TestExecutableInitVerifiesSelectedModelBeforeActivation(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	cwd := t.TempDir()
	server, calls := fixture(t)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	args := []string{"init", "--model", "gpt-6-luna", "--endpoint", server.URL + "/v1", "--credential", "env:QUAL_FIXTURE_API_KEY", "--accept", "--non-interactive", "--json"}
	out, diagnostic, err := invoke(t, binary, home, cwd, "", args...)
	if err != nil || !strings.Contains(out, `"accepted":true`) {
		t.Fatalf("verified init: %v %s %s", err, out, diagnostic)
	}
	// #nosec G304 -- Read the test-owned settings file to assert atomic initialization.
	original, err := os.ReadFile(filepath.Join(home, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	args[2] = "absent-model"
	out, diagnostic, err = invoke(t, binary, home, cwd, "", args...)
	if err == nil || !strings.Contains(diagnostic, "unsupported_capability") {
		t.Fatalf("unavailable model activated: %v %s %s", err, out, diagnostic)
	}
	// #nosec G304 -- Read the same test-owned settings file after failed activation.
	current, err := os.ReadFile(filepath.Join(home, "settings.yaml"))
	if err != nil || string(current) != string(original) {
		t.Fatalf("failed init changed active configuration: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("init made an inference request")
	}
}

func TestExecutableCorrectsMalformedFinish(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	server, _ := fixture(t, true)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	configure(t, home, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed finish fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, diagnostic, err := invoke(t, binary, home, repo, "", "review", "--json", "--non-interactive")
	if err != nil {
		t.Fatalf("corrected finish did not complete: %v %s %s", err, out, diagnostic)
	}
	var state domain.RunState
	if err = json.Unmarshal([]byte(out), &state); err != nil || state.Status != domain.StatusCompleted {
		t.Fatalf("finish state: %s %v", out, err)
	}
	archive, diagnostic, err := invoke(t, binary, home, repo, "", "sessions", "export", string(state.RunID))
	if err != nil || !strings.Contains(archive, "invalid-finish") {
		t.Fatalf("invalid finish evidence not saved: %v %s", err, diagnostic)
	}
}
