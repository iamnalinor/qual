//go:build llm

package live_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/session"
)

type liveFixture struct{ root, home, repo, binary, endpoint, token, ledger string }

func liveConfig(t *testing.T) liveFixture {
	t.Helper()
	if os.Getenv("QUAL_RUN_LLM") != "1" {
		t.Skip("live model calls require explicit QUAL_RUN_LLM=1")
	}
	f := liveFixture{root: os.Getenv("QUAL_LIVE_ARTIFACT_DIR"), endpoint: os.Getenv("QUAL_LIVE_GATE_URL"), token: os.Getenv("QUAL_LIVE_GATE_TOKEN"), ledger: os.Getenv("QUAL_LIVE_LEDGER")}
	validateGateURL(t, f.endpoint)
	var err error
	if !filepath.IsAbs(f.root) || !filepath.IsAbs(f.ledger) || len(f.token) < 32 {
		t.Fatal("absolute artifact directory/ledger and random gate token required")
	}
	if _, err = os.Stat(f.ledger); err != nil {
		t.Fatal("existing live ledger required")
	}
	if err = os.Mkdir(f.root, 0o700); err != nil {
		t.Fatal("artifact directory must be new:", err)
	}
	f.home = filepath.Join(f.root, "home")
	f.repo = filepath.Join(f.root, "source")
	f.binary = filepath.Join(f.root, "qual")
	for _, dir := range []string{f.home} {
		if err = os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f liveFixture) environment() []string {
	// Only the limited gate token enters the CLI. Upstream keys and ambient
	// credentials are never inherited by the reviewer or its tool subprocesses.
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + f.home, "QUAL_HOME=" + f.home, "QUAL_AUTH_BACKEND=file", "OPENAI_API_KEY=" + f.token, "GIT_CONFIG_NOSYSTEM=1"}
}

func fixtureCommand(t *testing.T, dir string, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...) //nolint:gosec // Test-owned fixture commands only.
	cmd.Dir = dir
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture command failed: %v: %s", err, output)
	}
}

func prepareLive(t *testing.T, f liveFixture) {
	t.Helper()
	fixtureCommand(t, "../..", os.Environ(), "go", "build", "-o", f.binary, "./cmd/qual")
	script, err := filepath.Abs("create-fixture.sh")
	if err != nil {
		t.Fatal(err)
	}
	fixtureCommand(t, f.root, f.environment(), script, f.repo)
	settings := fmt.Sprintf("schema_version: 1\nconnections:\n  live:\n    kind: openai\n    endpoint: %s\n    credential: env:OPENAI_API_KEY\nreviewers:\n  - id: reviewer\n    provider: live\n    model: gpt-6-luna\n    reasoning: low\n    soft: \"0.040000000\"\n    hard: \"0.120000000\"\nleader:\n  id: leader\n  provider: live\n  model: gpt-6-luna\n  reasoning: low\n  soft: \"0.040000000\"\n  hard: \"0.120000000\"\n", f.endpoint)
	writeLive(t, filepath.Join(f.home, "settings.yaml"), settings)
}

func writeLive(t *testing.T, path, content string) {
	t.Helper()
	// #nosec G703 -- Explicit test artifact directory, never model output paths.
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func startLive(t *testing.T, f liveFixture, label string, args ...string) (*exec.Cmd, <-chan error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), f.binary, args...) //nolint:gosec // Binary built by this test; fixed CLI arguments.
	cmd.Dir = f.repo
	cmd.Env = f.environment()
	// #nosec G304 -- Fixed trace filename inside test artifact directory.
	stdout, err := os.Create(filepath.Join(f.root, label+".stdout.json"))
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- Fixed trace filename inside test artifact directory.
	stderr, err := os.Create(filepath.Join(f.root, label+".stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); done <- errors.Join(err, stdout.Close(), stderr.Close()); close(done) }()
	return cmd, done
}

func interruptAfterTool(t *testing.T, f liveFixture) domain.RunState {
	t.Helper()
	cmd, done := startLive(t, f, "interrupted", "review", f.repo, "--worktree", "--non-interactive", "--json", "--output", filepath.Join(f.root, "interrupted-report"))
	store := session.New(f.home)

	ready := waitForSavedTool(t, store, done)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("interrupted CLI unexpectedly succeeded")
	}
	state, err := store.Load(t.Context(), ready.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != domain.StatusCancelled {
		t.Fatalf("interrupted state %s", state.Status)
	}
	return state
}

func waitForSavedTool(t *testing.T, store *session.Store, done <-chan error) domain.RunState {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("review exited before interruption: %v; inspect retained traces", err)
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-ticker.C:
			if state, ready := savedTool(t, store); ready {
				return state
			}
		}
	}
}

func savedTool(t *testing.T, store *session.Store) (domain.RunState, bool) {
	t.Helper()
	states, err := store.List(t.Context())
	if err != nil || len(states) != 1 {
		return domain.RunState{}, false
	}
	events, err := store.Events(t.Context(), states[0].RunID)
	return states[0], err == nil && hasToolResult(events)
}

func hasToolResult(events []domain.Event) bool {
	for _, event := range events {
		if event.Type == "tool_result" {
			return true
		}
	}
	return false
}

func TestGuardedReviewerLeaderResume(t *testing.T) {
	f := liveConfig(t)
	prepareLive(t, f)
	before := interruptAfterTool(t, f)
	snapshotLiveLedger(t, f, "budget-before-resume.json")
	store := session.New(f.home)
	priorEvents, err := store.Events(t.Context(), before.RunID)
	if err != nil {
		t.Fatal(err)
	}
	_, done := startLive(t, f, "resumed", "sessions", "resume", string(before.RunID), "--non-interactive", "--json", "--output", filepath.Join(f.root, "report"))
	if err := <-done; err != nil {
		t.Fatalf("live resume failed: %v; inspect %s", err, f.root)
	}
	after, err := store.Load(t.Context(), before.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Snapshot, after.Snapshot) {
		t.Fatal("resume changed fixed snapshot")
	}
	assertCompletedRoles(t, after)
	assertUsageRetained(t, before, after)
	events, err := store.Events(t.Context(), after.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateResumeLedgers(priorEvents, events); err != nil {
		t.Fatal(err)
	}
	assertExplorationAndFinish(t, events)
	if _, err = os.Stat(filepath.Join(f.root, "report", "report.json")); err != nil {
		t.Fatal(err)
	}
	snapshotLiveLedger(t, f, "budget-after.json")
	t.Logf("reports, traces, session and ledger retained at %s", f.root)
}

func assertCompletedRoles(t *testing.T, state domain.RunState) {
	t.Helper()
	if state.Status != domain.StatusCompleted || len(state.Results) != 2 {
		t.Fatalf("expected validated reviewer and leader completion: %s", state.Status)
	}
	for _, role := range state.Results {
		if role.Status != domain.StatusCompleted {
			t.Fatalf("role %s did not finish", role.AgentID)
		}
	}
}

func assertExplorationAndFinish(t *testing.T, events []domain.Event) {
	t.Helper()
	tools := map[domain.AgentID]map[string]bool{}
	for _, event := range events {
		if event.Type != "tool_result" {
			continue
		}
		var outcome struct {
			Call domain.ToolCall `json:"call"`
		}
		if err := json.Unmarshal(event.Data, &outcome); err != nil {
			t.Fatal(err)
		}
		if tools[event.AgentID] == nil {
			tools[event.AgentID] = map[string]bool{}
		}
		tools[event.AgentID][outcome.Call.Name] = true
	}
	for _, role := range []domain.AgentID{"reviewer", "leader"} {
		used := tools[role]
		if !used["read_file"] || !used["write_file"] || !used["finish"] {
			t.Fatalf("role %s missing exploration/output/finish tools: %v", role, used)
		}
	}
}

func validateGateURL(t *testing.T, endpoint string) {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.Path != "/v1" {
		t.Fatal("QUAL_LIVE_GATE_URL must be http://127.0.0.1:PORT/v1")
	}
}

func snapshotLiveLedger(t *testing.T, f liveFixture, name string) {
	t.Helper()
	data, err := os.ReadFile(f.ledger)
	if err != nil {
		t.Fatal(err)
	}
	writeLive(t, filepath.Join(f.root, name), string(data))
}

func assertUsageRetained(t *testing.T, before, after domain.RunState) {
	t.Helper()
	prior := map[domain.AgentID]domain.Money{}
	for _, result := range before.Results {
		prior[result.AgentID] = result.Usage.Cost
	}
	for _, result := range after.Results {
		if result.Usage.Cost < prior[result.AgentID] {
			t.Fatal("resume reset accumulated role cost")
		}
	}
}
