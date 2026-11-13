package citemplate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitLabRepeatedPipelinesUseFreshArtifactWorkspaces(t *testing.T) {
	files, err := Generate("gitlab")
	if err != nil {
		t.Fatal(err)
	}

	for _, job := range []string{"qual-authorize", "qual-review"} {
		t.Run(job, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "runner-build")
			for pipeline := 1; pipeline <= 2; pipeline++ {
				// Separate runners can reuse each job's disk path across pipelines.
				// Runner preparation precedes this pipeline's artifact download.
				prepareGitLabFixtureWorkspace(t, workspace, gitlabJobStrategy(t, files.Workflow, job))
				runGitLabWorkspaceFixture(t, workspace, job, fmt.Sprintf("pipeline-%d", pipeline))
			}
		})
	}
}

func runGitLabWorkspaceFixture(t *testing.T, workspace, job, authorization string) {
	t.Helper()
	trusted := filepath.Join(workspace, "qual-trusted")
	if job == "qual-authorize" {
		if err := os.Mkdir(trusted, 0o700); err != nil {
			t.Fatalf("authorization cannot create fresh trusted directory: %v", err)
		}
		return
	}
	writeWorkspaceAuthorization(t, trusted, authorization)
	if err := os.Mkdir(filepath.Join(workspace, "qual-output"), 0o700); err != nil {
		t.Fatalf("sandbox output already exists: %v", err)
	}
	restored, err := os.ReadFile(filepath.Clean(filepath.Join(trusted, "authorization.json")))
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != authorization {
		t.Fatalf("stale authorization: %q", restored)
	}
}

func gitlabJobStrategy(t *testing.T, workflow, job string) string {
	t.Helper()
	_, section, ok := strings.Cut(workflow, "\n"+job+":\n")
	if !ok {
		t.Fatalf("missing job %s", job)
	}
	for _, line := range strings.Split(section, "\n") {
		if line != "" && !strings.HasPrefix(line, " ") {
			break
		}
		if value, ok := strings.CutPrefix(line, "    GIT_STRATEGY: "); ok {
			return value
		}
	}
	t.Fatalf("job %s lacks explicit workspace strategy", job)
	return ""
}

func prepareGitLabFixtureWorkspace(t *testing.T, workspace, strategy string) {
	t.Helper()
	switch strategy {
	case "empty":
		if err := os.RemoveAll(workspace); err != nil {
			t.Fatal(err)
		}
	case "none": // Reuses old files; the repeated-job regression must reject this.
	default:
		t.Fatalf("unsupported checkout-free fixture strategy %q", strategy)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeWorkspaceAuthorization(t *testing.T, trusted, value string) {
	t.Helper()
	if err := os.MkdirAll(trusted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trusted, "authorization.json"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
