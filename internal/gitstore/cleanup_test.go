package gitstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestCleanupExplicitRetentionPolicy(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		ttl       time.Duration
		removed   int
		code      domain.ErrorCode
		keepFresh bool
	}{
		{name: "manual_zero_removes_all_terminal", removed: 2},
		{name: "automatic_fourteen_days", ttl: 14 * 24 * time.Hour, removed: 1, keepFresh: true},
		{name: "negative_ttl_is_invalid", ttl: -time.Nanosecond, code: "invalid_config", keepFresh: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, paths := cleanupPolicyFixture(t, now)
			removed, e := s.Cleanup(context.Background(), now, tc.ttl)
			if domain.CodeOf(e) != tc.code {
				t.Fatalf("error %v, want code %s", e, tc.code)
			}
			if len(removed) != tc.removed {
				t.Fatalf("removed %v, want %d", removed, tc.removed)
			}
			assertCleanupPath(t, paths["active"], true)
			assertCleanupPath(t, paths["pinned"], true)
			assertCleanupPath(t, paths["fresh"], tc.keepFresh)
			assertCleanupPath(t, paths["old"], tc.code != "")
		})
	}
}

func cleanupPolicyFixture(t *testing.T, now time.Time) (*Source, map[string]string) {
	t.Helper()
	ctx := context.Background()
	r := repoTest(t)
	writeTest(t, r, "file", "dirty")
	s := New(t.TempDir())
	snap, e := s.Prepare(ctx, domain.SourceRequest{Target: r, RunID: "cleanup-policy"})
	if e != nil {
		t.Fatal(e)
	}
	old := now.Add(-30 * 24 * time.Hour)
	fresh := now.Add(-time.Minute)
	entries := []struct {
		name      string
		completed *time.Time
		pinned    bool
	}{{name: "active"}, {name: "pinned", completed: &old, pinned: true}, {name: "old", completed: &old}, {name: "fresh", completed: &fresh}}
	paths := map[string]string{}
	for _, entry := range entries {
		path, e := s.Worktree(ctx, snap, domain.AgentID(entry.name))
		if e != nil {
			t.Fatal(e)
		}
		if e = s.Retain(ctx, snap.RunID, domain.AgentID(entry.name), entry.completed, entry.pinned); e != nil {
			t.Fatal(e)
		}
		paths[entry.name] = path
	}
	return s, paths
}

func assertCleanupPath(t *testing.T, path string, wantPresent bool) {
	t.Helper()
	for _, candidate := range []string{path, path + ".json"} {
		_, e := os.Stat(candidate)
		if wantPresent && e != nil {
			t.Fatalf("expected retained %s: %v", filepath.Base(candidate), e)
		}
		if !wantPresent && !os.IsNotExist(e) {
			t.Fatalf("expected removed %s: %v", filepath.Base(candidate), e)
		}
	}
}
