package live_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoFixtureExistingTestDetectsSeed(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "source")
	// #nosec G204 -- Fixed fixture script with test-owned temporary repository path.
	create := exec.CommandContext(t.Context(), "./create-fixture.sh", repo)
	if output, err := create.CombinedOutput(); err != nil {
		t.Fatalf("create fixture: %v %s", err, output)
	}
	test := exec.CommandContext(t.Context(), "go", "test", "-count=1", "./...")
	test.Dir = repo
	output, err := test.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "TestDivideZero") || !strings.Contains(string(output), "integer divide by zero") {
		t.Fatalf("seed was not caught by meaningful committed test: %v %s", err, output)
	}
	restore := exec.CommandContext(t.Context(), "git", "restore", "divide.go")
	restore.Dir = repo
	if output, err = restore.CombinedOutput(); err != nil {
		t.Fatalf("restore base: %v %s", err, output)
	}
	base := exec.CommandContext(t.Context(), "go", "test", "-count=1", "./...")
	base.Dir = repo
	if output, err = base.CombinedOutput(); err != nil {
		t.Fatalf("base test must pass: %v %s", err, output)
	}
}
