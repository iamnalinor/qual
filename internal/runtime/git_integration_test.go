package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/gitstore"
	"github.com/iamnalinor/qual/internal/port"
	"github.com/iamnalinor/qual/internal/session"
)

func gitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	ctx := context.Background()
	commands := [][]string{{"init", "-q"}, {"config", "user.name", "Fixture"}, {"config", "user.email", "fixture@example.test"}}
	for _, args := range commands {
		cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // Test-owned constant git fixture argument arrays.
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture %s: %v", output, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "code.go"), []byte("package sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "code.go"}, {"commit", "-qm", "initial"}} {
		cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // Test-owned constant git fixture argument arrays.
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit fixture %s: %v", output, err)
		}
	}
	return root
}

func TestRealGitReviewAndCleanSetup(t *testing.T) {
	ctx := context.Background()
	root := gitFixture(t)
	home := t.TempDir()
	p := &scripted{t: t}
	rt := New(Config{Home: home, Source: gitstore.New(home), Store: session.New(home), Providers: map[string]port.Provider{"fixture": p}})
	agents := []domain.AgentSpec{{ID: "primary", Role: domain.RoleReviewer, Provider: "fixture"}, {ID: "leader", Role: domain.RoleLeader, Provider: "fixture"}}
	noChanges, err := rt.Review(ctx, domain.SourceRequest{RunID: "empty", Target: root}, agents)
	if err != nil || noChanges.Status != domain.StatusNoChanges || p.calls != 0 {
		t.Fatalf("empty diff called model: %s %v", noChanges.Status, err)
	}
	setup, err := rt.Setup(ctx, domain.SourceRequest{RunID: "setup", Target: root}, domain.AgentSpec{ID: "setup", Provider: "fixture"})
	if err != nil || setup.Status != domain.StatusCompleted {
		t.Fatalf("clean setup: %s %v", setup.Status, err)
	}
	if err = os.WriteFile(filepath.Join(root, "code.go"), []byte("package changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := rt.Review(ctx, domain.SourceRequest{RunID: "dirty", Target: root, Worktree: true}, agents)
	if err != nil || state.Status != domain.StatusCompleted {
		t.Fatalf("real git review: %s %v", state.Status, err)
	}
	data, err := os.ReadFile(filepath.Join(root, "code.go")) //nolint:gosec // Test-owned repository fixture path.
	if err != nil || string(data) != "package changed\n" {
		t.Fatalf("runtime changed source: %q %v", data, err)
	}
}
