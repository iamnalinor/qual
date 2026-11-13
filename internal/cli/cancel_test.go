package cli_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestExecutableCancelResumeImmutableConfiguration(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	good, calls := fixture(t)
	defer good.Close()
	blocking, entered, resume := blockingFixture(t, good)

	configure(t, home, blocking.URL+"/v1")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed cancellation fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cancelled := cancelReview(t, binary, home, repo, entered)
	id := string(cancelled.RunID)
	var err error
	// The immutable endpoint remains selected even when current configuration becomes invalid.
	if err = os.WriteFile(filepath.Join(home, "settings.yaml"), []byte("unknown_setting: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resume.Store(true)
	out, diagnostic, err := invoke(t, binary, home, repo, "", "sessions", "resume", id, "--json", "--non-interactive")
	if err != nil {
		t.Fatalf("resume: %v %s %s", err, out, diagnostic)
	}
	var resumed domain.RunState
	if err = json.Unmarshal([]byte(out), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Status != domain.StatusCompleted || resumed.Snapshot.DiffHash != cancelled.Snapshot.DiffHash || calls.Load() == 0 || !strings.Contains(out, "changed cancellation fixture") {
		t.Fatalf("resume did not preserve snapshot: %s", out)
	}
}

func cancelledState(t *testing.T, stdout []byte, stderr string) domain.RunState {
	t.Helper()
	var state domain.RunState
	if err := json.Unmarshal(stdout, &state); err != nil || state.Status != domain.StatusCancelled {
		t.Fatalf("cancelled output: %s %s %v", stdout, stderr, err)
	}
	return state
}

func blockingFixture(t *testing.T, good *httptest.Server) (*httptest.Server, chan struct{}, *atomic.Bool) {
	t.Helper()
	entered := make(chan struct{}, 1)
	var resume atomic.Bool
	release := make(chan struct{})
	blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if resume.Load() {
			good.Config.Handler.ServeHTTP(w, r)
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))

	t.Cleanup(func() { close(release); blocking.Close() })
	return blocking, entered, &resume
}

func cancelReview(t *testing.T, binary, home, repo string, entered chan struct{}) domain.RunState {
	t.Helper()
	// #nosec G204 -- Run the test-built CLI with fixed arguments against test-owned repository/storage.
	cmd := exec.CommandContext(t.Context(), binary, "review", "--json", "--non-interactive")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "QUAL_HOME="+home, "QUAL_AUTH_BACKEND=file")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("review did not reach provider")
	}
	entries, err := os.ReadDir(filepath.Join(home, "sessions"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("session missing %v %v", entries, err)
	}
	id := entries[0].Name()
	out, diagnostic, err := invoke(t, binary, home, repo, "", "sessions", "cancel", id, "--json")
	if err != nil {
		t.Fatalf("cancel: %v %s %s", err, out, diagnostic)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("cancelled process exited successfully")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled process did not exit")
	}
	return cancelledState(t, stdout.Bytes(), stderr.String())
}
