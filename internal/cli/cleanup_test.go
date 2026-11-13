package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/gitstore"
	"github.com/iamnalinor/qual/internal/session"
)

func cleanupWorkspace(t *testing.T, home, repo, id string, age time.Duration, active, pinned bool) string {
	t.Helper()
	source := gitstore.New(home)
	snapshot, err := source.Prepare(t.Context(), domain.SourceRequest{Target: repo, Home: home, RunID: domain.RunID(id), AllowEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	path, err := source.Worktree(t.Context(), snapshot, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	var completed *time.Time
	if !active {
		stamp := time.Now().Add(-age)
		completed = &stamp
	}
	if err = source.Retain(t.Context(), domain.RunID(id), "reviewer", completed, pinned); err != nil {
		t.Fatal(err)
	}
	store := session.New(home)
	if err = store.Create(t.Context(), domain.RunState{SchemaVersion: 1, RunID: domain.RunID(id), Snapshot: snapshot, Status: domain.StatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.PutArtifact(t.Context(), domain.RunID(id), "evidence", strings.NewReader("retained evidence")); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecutableAutomaticAndManualCleanupPolicies(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	configure(t, home, "http://127.0.0.1:1/v1")
	old := cleanupWorkspace(t, home, repo, "old", 15*24*time.Hour, false, false)
	fresh := cleanupWorkspace(t, home, repo, "fresh", time.Hour, false, false)
	active := cleanupWorkspace(t, home, repo, "active", 0, true, false)
	pinned := cleanupWorkspace(t, home, repo, "pinned", 30*24*time.Hour, false, true)
	out, diagnostic, err := invoke(t, binary, home, repo, "", "doctor", "--json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("doctor %v %s %s", err, out, diagnostic)
	}
	assertWorktreePresence(t, old, false)
	assertWorktreePresence(t, fresh, true)
	assertWorktreePresence(t, active, true)
	assertWorktreePresence(t, pinned, true)
	out, diagnostic, err = invoke(t, binary, home, repo, "", "cleanup", "--json")
	if err != nil {
		t.Fatalf("cleanup %v %s %s", err, out, diagnostic)
	}
	assertRemovedCount(t, out, 1)
	assertWorktreePresence(t, fresh, false)
	assertWorktreePresence(t, active, true)
	assertWorktreePresence(t, pinned, true)
	assertRetainedEvidence(t, home, []string{"old", "fresh", "active", "pinned"})
	if _, err = os.Stat(filepath.Join(home, "settings.yaml")); err != nil {
		t.Fatalf("cleanup removed settings: %v", err)
	}
}

func TestExecutableManualCleanupSkipsAutomaticAgePass(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	repo := repository(t)
	cleanupWorkspace(t, home, repo, "old", 15*24*time.Hour, false, false)
	cleanupWorkspace(t, home, repo, "fresh", time.Minute, false, false)
	out, diagnostic, err := invoke(t, binary, home, repo, "", "cleanup", "--json")
	if err != nil {
		t.Fatalf("cleanup %v %s %s", err, out, diagnostic)
	}
	assertRemovedCount(t, out, 2)
}

func TestExecutableAutomaticCleanupWarningDoesNotHideCommand(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	cwd := t.TempDir()
	registry := filepath.Join(home, "worktrees", "broken")
	if err := os.MkdirAll(registry, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registry, "reviewer.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, diagnostic, err := invoke(t, binary, home, cwd, "", "doctor", "--json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(diagnostic, "automatic_cleanup_failed") {
		t.Fatalf("GC hid command output %v %s %s", err, out, diagnostic)
	}
	diagnosticJSONLines(t, diagnostic)
	_, diagnostic, err = invoke(t, binary, home, cwd, "", "cleanup", "--json")
	if err == nil {
		t.Fatal("manual cleanup failure was hidden")
	}
	diagnosticJSONLines(t, diagnostic)
}

func assertWorktreePresence(t *testing.T, path string, present bool) {
	t.Helper()
	_, err := os.Stat(path)
	if present && err != nil {
		t.Fatalf("retained workspace missing %s: %v", path, err)
	}
	if !present && !os.IsNotExist(err) {
		t.Fatalf("disposable workspace remains %s: %v", path, err)
	}
}

func assertRemovedCount(t *testing.T, out string, want int) {
	t.Helper()
	var value struct {
		Removed []string `json:"removed"`
	}
	if err := json.Unmarshal([]byte(out), &value); err != nil || len(value.Removed) != want {
		t.Fatalf("removed count want%d: %s %v", want, out, err)
	}
}

func assertRetainedEvidence(t *testing.T, home string, ids []string) {
	t.Helper()
	for _, id := range ids {
		if _, err := session.New(home).Load(t.Context(), domain.RunID(id)); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(filepath.Join(home, "sessions", id, "artifacts"))
		if err != nil || len(entries) == 0 {
			t.Fatalf("evidence removed for%s: %v", id, err)
		}
	}
}
