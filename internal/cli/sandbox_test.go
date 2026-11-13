//go:build sandbox

package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestSandboxResponsesReview(t *testing.T) {
	binary := buildCLI(t)
	server, calls := fixture(t, false, true)
	defer server.Close()
	home := t.TempDir()
	repo := repository(t)
	configure(t, home, server.URL+"/v1")
	toolchain := goToolchain(t)
	addGoToolFixture(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed sandbox source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(home, "key")
	if err := os.WriteFile(credential, []byte("fixture-only-credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "sandbox-output")
	// #nosec G204 -- Fixed reviewed launcher runs the test-built Qual executable against test-owned Git and HTTP fixtures.
	cmd := exec.CommandContext(t.Context(), "../../scripts/qual-sandbox", "--qual", binary, "--input", repo, "--output", output, "--network", "open", "--toolchain", toolchain, "--settings", filepath.Join(home, "settings.yaml"), "--credential", "QUAL_FIXTURE_API_KEY="+credential, "--", "review", ".", "--worktree", "--json", "--non-interactive", "--output", "/output")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("sandbox review: %v\n%s\n%s", err, stdout.String(), stderr.String())
	}
	var state domain.RunState
	if err := json.Unmarshal(stdout.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.StatusCompleted || len(state.Results) != 2 || calls.Load() < 10 {
		t.Fatalf("sandbox roles did not finish: %+v calls=%d", state, calls.Load())
	}
	verifySandboxEvidence(t, repo, output, stdout.String(), stderr.String())
}

func goToolchain(t *testing.T) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "go", "env", "GOROOT")
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func addGoToolFixture(t *testing.T, repo string) {
	t.Helper()
	files := map[string]string{"go.mod": "module fixture\n\ngo 1.27.4\n", "environment_test.go": `package fixture
import("os";"path/filepath";"testing")
func TestControlledEnvironment(t *testing.T){
 for _,name:=range []string{"HOME","GOCACHE","TMPDIR"}{if os.Getenv(name)==""{t.Fatalf("missing controlled %s",name)}}
 if os.Getenv("QUAL_FIXTURE_API_KEY")!=""{t.Fatal("credential inherited by tool")}
 if _,err:=os.UserCacheDir();err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(filepath.Join(os.Getenv("HOME"),"probe"),[]byte("fixture"),0600);err!=nil{t.Fatal(err)}
}
`}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", "go.mod", "environment_test.go")
}

func verifySandboxEvidence(t *testing.T, repo, output, stdout, stderr string) {
	t.Helper()
	if !strings.Contains(stderr, "launcher-reported active") || !strings.Contains(stderr, `"network":"open"`) {
		t.Fatalf("sandbox metadata missing: %s", stderr)
	}
	if strings.Contains(stdout+stderr, "fixture-only-credential") {
		t.Fatal("sandbox leaked credential")
	}
	// #nosec G304 -- Read the test-owned source to verify launcher isolation.
	data, err := os.ReadFile(filepath.Join(repo, "file.txt"))
	if err != nil || string(data) != "changed sandbox source\n" {
		t.Fatalf("sandbox modified caller: %q %v", data, err)
	}
	if _, err = os.Stat(filepath.Join(output, "reports", "report.json")); err != nil {
		t.Fatal(err)
	}
}
