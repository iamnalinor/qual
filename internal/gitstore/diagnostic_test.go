package gitstore

import (
	"context"
	"strings"
	"testing"
)

func TestGitFailureReportsOperationAndReason(t *testing.T) {
	_, err := git(context.Background(), t.TempDir(), nil, nil, "rev-parse", "--show-toplevel")
	if err == nil || !strings.Contains(err.Error(), "git rev-parse") || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("missing Git reason: %v", err)
	}
}

func TestGitFailureRedactsConfigSecret(t *testing.T) {
	root := t.TempDir()
	gitTest(t, root, "init", "-q")
	extra := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.bare", "GIT_CONFIG_VALUE_0=synthetic-secret"}
	_, err := git(context.Background(), root, nil, extra, "rev-parse", "--show-toplevel")
	if err == nil || strings.Contains(err.Error(), "synthetic-secret") || !strings.Contains(err.Error(), "bad boolean") {
		t.Fatalf("unsafe or missing Git reason: %v", err)
	}
}
