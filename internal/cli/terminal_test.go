//go:build linux || darwin

package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

type terminalCapture struct {
	sync.Mutex
	buffer bytes.Buffer
}

func (b *terminalCapture) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.buffer.Write(p)
}
func (b *terminalCapture) text() string { b.Lock(); defer b.Unlock(); return b.buffer.String() }

type terminalProcess struct {
	file   *os.File
	output *terminalCapture
	done   chan error
}

func startTerminal(t *testing.T, binary, home, cwd string, width uint16, args ...string) *terminalProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	t.Cleanup(cancel)
	// #nosec G204 -- Test-built CLI executable and fixture arguments.
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "QUAL_HOME="+home, "QUAL_AUTH_BACKEND=file", "TERM=xterm-256color", "NO_COLOR=1")
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: width})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Log(err)
		}
	})
	p := &terminalProcess{file: file, output: &terminalCapture{}, done: make(chan error, 1)}
	go func() {
		if _, err := io.Copy(p.output, file); err != nil && !errors.Is(err, syscall.EIO) && !errors.Is(err, os.ErrClosed) {
			t.Errorf("terminal read: %v", err)
		}
	}()
	go func() { p.done <- cmd.Wait() }()
	return p
}

func (p *terminalProcess) expect(t *testing.T, text string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(p.output.text(), text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("terminal never displayed %q:\n%s", text, p.output.text())
}

func (p *terminalProcess) send(t *testing.T, text string) {
	t.Helper()
	if _, err := io.WriteString(p.file, text); err != nil {
		t.Fatal(err)
	}
}

func (p *terminalProcess) wait(t *testing.T, success bool) {
	t.Helper()
	select {
	case err := <-p.done:
		if (err == nil) != success {
			t.Fatalf("terminal exit: %v\n%s", err, p.output.text())
		}
	case <-time.After(12 * time.Second):
		t.Fatalf("terminal did not exit:\n%s", p.output.text())
	}
}

func TestTerminalOnboardingAndHiddenKey(t *testing.T) {
	binary := buildCLI(t)
	server, _ := fixture(t)
	defer server.Close()
	home := t.TempDir()
	p := startTerminal(t, binary, home, t.TempDir(), 80, "init", "--kind", "openai", "--endpoint", server.URL+"/v1")
	p.expect(t, "API key")
	p.send(t, "fixture-only-credential\r")
	p.expect(t, "Choose the reviewer model")
	p.send(t, "\r")
	p.expect(t, "Who should verify")
	p.send(t, "\r")
	p.expect(t, "Set a spending limit")
	p.send(t, "\r")
	p.expect(t, "Interface language")
	p.send(t, "\r")
	p.expect(t, "Language for findings")
	p.send(t, "\r")
	p.expect(t, "User settings proposal")
	p.send(t, "\r")
	p.expect(t, "Activate these settings")
	p.send(t, "\r")
	p.wait(t, true)
	if strings.Contains(p.output.text(), "fixture-only-credential") {
		t.Fatal("secret leaked to terminal")
	}
	if _, err := os.Stat(filepath.Join(home, "settings.yaml")); err != nil {
		t.Fatal(err)
	}
	p.expect(t, "qual review")
}

func TestTerminalReviewTimelineAndCompletion(t *testing.T) {
	binary := buildCLI(t)
	server, _ := fixture(t)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	home, repo := t.TempDir(), repository(t)
	configure(t, home, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := startTerminal(t, binary, home, repo, 120, "review")
	p.wait(t, true)
	p.expect(t, "ctrl+o")
	p.expect(t, "leader")
	p.expect(t, "qual sessions show")
	if strings.Contains(p.output.text(), "cost nanodollars") {
		t.Fatal("raw ledger leaked into terminal summary")
	}
}

func TestTerminalProviderMenuCancellationDoesNotActivate(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	p := startTerminal(t, binary, home, t.TempDir(), 80, "init")
	p.expect(t, "How would you like to connect")
	p.send(t, "\x1b[B")
	p.send(t, "\x1b")
	p.wait(t, false)
	if _, err := os.Stat(filepath.Join(home, "settings.yaml")); !os.IsNotExist(err) {
		t.Fatalf("cancelled wizard activated settings: %v", err)
	}
}

func TestTerminalQuestionUsesSameInputOwner(t *testing.T) {
	binary := buildCLI(t)
	server, _ := fixture(t, false, false, true)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	home, repo := t.TempDir(), repository(t)
	configure(t, home, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed question fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := startTerminal(t, binary, home, repo, 80, "review", "--ui-language", "ru")
	p.expect(t, "(reviewer)")
	p.send(t, "fixture answer\r")
	// Each role independently asks the fixture question. Wait for the leader before answering it.
	p.expect(t, "(leader)")
	p.send(t, "fixture answer\r")
	p.wait(t, true)
	p.expect(t, "qual sessions show")
}

func TestTerminalCancelResizeAndResume(t *testing.T) {
	binary := buildCLI(t)
	good, _ := fixture(t)
	defer good.Close()
	blocking, entered, resume := blockingFixture(t, good)
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	home, repo := t.TempDir(), repository(t)
	configure(t, home, blocking.URL+"/v1")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed cancellation fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := startTerminal(t, binary, home, repo, 80, "review")
	select {
	case <-entered:
	case <-time.After(8 * time.Second):
		t.Fatal("review never reached provider")
	}
	p.expect(t, "ctrl+o")
	p.send(t, "\x0f")
	if err := pty.Setsize(p.file, &pty.Winsize{Rows: 35, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	p.send(t, "\x1b")
	p.wait(t, false)
	p.expect(t, "qual sessions resume")
	entries, err := os.ReadDir(filepath.Join(home, "sessions"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("saved session: %v %v", entries, err)
	}
	resume.Store(true)
	next := startTerminal(t, binary, home, repo, 120, "sessions", "resume", entries[0].Name())
	next.wait(t, true)
	next.expect(t, "qual sessions show")
}

func TestTerminalProviderFailureRestoresAndReportsCause(t *testing.T) {
	binary := buildCLI(t)
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	home, repo := t.TempDir(), repository(t)
	configure(t, home, "http://127.0.0.1:1/v1")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed failure fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := startTerminal(t, binary, home, repo, 80, "review")
	p.wait(t, false)
	p.expect(t, "connect: connection refused")
	p.expect(t, "qual sessions resume")
	if !strings.Contains(p.output.text(), "\x1b[?25h") {
		t.Fatal("terminal cursor was not restored")
	}
}

func TestExecutableProjectModelWithoutLocalProviderPointsToInit(t *testing.T) {
	binary := buildCLI(t)
	home, repo := t.TempDir(), repository(t)
	configure(t, home, "http://127.0.0.1:1/v1")
	path := filepath.Join(repo, ".qual")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "settings.yaml"), []byte("reviewers:\n  - id: security\n    provider: team-account\n    model: required-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "Project review settings")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed project model fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, diagnostic, err := invoke(t, binary, home, repo, "", "review", "--non-interactive")
	if err == nil {
		t.Fatal("missing project provider accepted")
	}
	for _, want := range []string{"required-model", "team-account", ".qual/settings.yaml", "qual init --connection team-account --model required-model --add-account"} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("missing %q in %s", want, diagnostic)
		}
	}
}

func TestExecutableProjectModelMissingTokenNamesModelAndOrigin(t *testing.T) {
	binary := buildCLI(t)
	home, repo := t.TempDir(), repository(t)
	configure(t, home, "http://127.0.0.1:1/v1")
	t.Setenv("QUAL_FIXTURE_API_KEY", "")
	dir := filepath.Join(repo, ".qual")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte("reviewers:\n  - id: project-reviewer\n    provider: primary\n    model: gpt-6-luna\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-qm", "Select project model")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed token fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, diagnostic, err := invoke(t, binary, home, repo, "", "review", "--non-interactive")
	if err == nil {
		t.Fatal("missing token accepted")
	}
	for _, want := range []string{"gpt-6-luna", "primary", ".qual/settings.yaml", "environment variable is empty", "qual init --connection primary --kind openai --model gpt-6-luna --add-account"} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("missing %q: %s", want, diagnostic)
		}
	}
}

func TestExecutableAutomationDoesNotReadOpenPromptPipe(t *testing.T) {
	binary := buildCLI(t)
	server, _ := fixture(t, false, false, true)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	home, repo := t.TempDir(), repository(t)
	configure(t, home, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("changed automation question\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"review", "--json", "--ui-language", "ru"}, {"review", "--ui-language", "ru"}, {"init", "--json", "--kind", "openai", "--connection", "primary", "--model", "gpt-6-luna", "--endpoint", server.URL + "/v1", "--credential", "env:QUAL_FIXTURE_API_KEY"}} {
		heldOpenPipe(t, binary, home, repo, args)
	}
}

func heldOpenPipe(t *testing.T, binary, home, repo string, args []string) {
	t.Helper()
	input, hold, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := input.Close(); err != nil {
			t.Log(err)
		}
		if err := hold.Close(); err != nil {
			t.Log(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	// #nosec G204 -- Test-built executable, fixed arguments and local provider fixture.
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "QUAL_HOME="+home, "QUAL_AUTH_BACKEND=file")
	cmd.Stdin = input
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("command blocked on held-open prompt pipe: %v\n%s", args, output.String())
	}
	verifyPipeOutput(t, args, output.String(), err)
}

func verifyPipeOutput(t *testing.T, args []string, output string, err error) {
	t.Helper()
	if args[0] == "review" && !strings.Contains(output, "ask_user") {
		t.Fatalf("fixture question missing: %v %s", err, output)
	}
	if len(args) > 1 && args[1] == "--json" && args[0] == "review" && !strings.Contains(output, `"available":false`) {
		t.Fatalf("question availability not structured: %s", output)
	}
	if args[0] == "init" && err != nil {
		t.Fatalf("preview failed: %v %s", err, output)
	}
}

func TestTerminalAuthLoginKeepsInteractiveConfigurationInInit(t *testing.T) {
	binary := buildCLI(t)
	home := t.TempDir()
	p := startTerminal(t, binary, home, t.TempDir(), 80, "auth", "login", "--kind", "openai", "--profile", "personal")
	p.wait(t, false)
	p.expect(t, "qual init")
	if strings.Contains(p.output.text(), "Paste your key") || strings.Contains(p.output.text(), "API key:") {
		t.Fatal("auth login opened a separate interactive setup")
	}
}

func TestExecutableInitAddsAccountWithoutReplacingDefaults(t *testing.T) {
	binary := buildCLI(t)
	server, _ := fixture(t)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	home := t.TempDir()
	configure(t, home, server.URL+"/v1")
	_, diagnostic, err := invoke(t, binary, home, t.TempDir(), "", "init", "--connection", "team", "--kind", "openai", "--endpoint", server.URL+"/v1", "--credential", "env:QUAL_FIXTURE_API_KEY", "--model", "gpt-6-luna", "--add-account", "--accept", "--non-interactive")
	if err != nil {
		t.Fatalf("adding account: %v %s", err, diagnostic)
	}
	// #nosec G304 -- Inspect the test-owned initialization result under a temporary home.
	data, err := os.ReadFile(filepath.Join(home, "settings.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"primary:", "team:", "provider: primary"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("init discarded %q: %s", want, data)
		}
	}
}

func TestTerminalSetupShowsActualRoleAndDeclinedProposal(t *testing.T) {
	binary := buildCLI(t)
	server, _ := fixture(t)
	defer server.Close()
	t.Setenv("QUAL_FIXTURE_API_KEY", "fixture-only-credential")
	home, repo := t.TempDir(), repository(t)
	configure(t, home, server.URL+"/v1")
	path := filepath.Join(home, "settings.yaml")
	// #nosec G304 -- Rewrite only the test-owned global settings fixture.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(data), "model: gpt-6-luna", "model: review-model-not-used", 2)
	// #nosec G703 -- The destination is the fixed settings file under the test-created temporary home.
	if err = os.WriteFile(path, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	p := startTerminal(t, binary, home, repo, 80, "setup")
	p.expect(t, "How should Qual treat this project?")
	p.send(t, "\r")
	p.expect(t, "setup: gpt-6-luna")
	p.expect(t, "Proposed personal settings and rules")
	p.send(t, "\r")
	p.expect(t, "Apply these personal settings and rules?")
	p.send(t, "\x1b[B\r")
	p.wait(t, true)
	p.expect(t, "Proposal declined")
	if strings.Contains(p.output.text(), "review-model-not-used") {
		t.Fatal("setup displayed unused review models")
	}
}

func TestTerminalRussianOnboardingUsesLocalizedControls(t *testing.T) {
	binary := buildCLI(t)
	t.Setenv("QUAL_UI_LANGUAGE", "ru")
	p := startTerminal(t, binary, t.TempDir(), t.TempDir(), 80, "init")
	p.expect(t, "Как подключить провайдера?")
	p.expect(t, "выбрать")
	p.send(t, "\x1b")
	p.wait(t, false)
}

func TestTerminalInitReconnectsOriginalCredentialForResume(t *testing.T) {
	binary := buildCLI(t)
	server, _ := fixture(t)
	defer server.Close()
	for _, missing := range []bool{false, true} {
		testReconnectCredential(t, binary, server.URL, missing)
	}
}

func testReconnectCredential(t *testing.T, binary, endpoint string, missing bool) {
	t.Helper()
	home := t.TempDir()
	configure(t, home, endpoint+"/v1")
	settingsPath := filepath.Join(home, "settings.yaml")
	// #nosec G304 -- Inspect and rewrite the test-owned global settings fixture.
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	before = bytes.ReplaceAll(before, []byte("env:QUAL_FIXTURE_API_KEY"), []byte("profile:personal"))
	// #nosec G703 -- Fixed settings file under this test-created temporary home.
	if err = os.WriteFile(settingsPath, before, 0o600); err != nil {
		t.Fatal(err)
	}
	if !missing {
		_, diagnostic, e := invoke(t, binary, home, t.TempDir(), "obsolete-fixture-key\n", "auth", "login", "--kind", "openai", "--profile", "personal", "--stdin")
		if e != nil {
			t.Fatalf("credential fixture: %v %s", e, diagnostic)
		}
	}
	p := startTerminal(t, binary, home, t.TempDir(), 80, "init", "--kind", "openai", "--connection", "primary", "--endpoint", endpoint+"/v1", "--model", "gpt-6-luna")
	p.expect(t, "Which account?")
	if !missing {
		p.send(t, "\x1b[B")
	}
	p.send(t, "\r")
	p.expect(t, "API key")
	p.send(t, "fixture-only-credential\r")
	p.expect(t, "Who should verify")
	p.send(t, "\x1b")
	p.wait(t, false)
	// #nosec G304 -- Assert the repaired test credential uses the original immutable reference.
	record, e := os.ReadFile(filepath.Join(home, "auth", "personal.json"))
	if e != nil || !bytes.Contains(record, []byte("fixture-only-credential")) {
		t.Fatalf("original credential was not repaired: %v", e)
	}
	// #nosec G304 -- Compare the fixed test settings path after cancellation.
	after, e := os.ReadFile(settingsPath)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatalf("cancelled init changed active settings: %v", e)
	}
	if strings.Contains(p.output.text(), "fixture-only-credential") {
		t.Fatal("reconnected key leaked")
	}
}

func TestTerminalInitDoesNotRebindAnotherAccountCredential(t *testing.T) {
	binary := buildCLI(t)
	server, _ := fixture(t)
	defer server.Close()
	t.Run("different-provider", func(t *testing.T) { testCredentialAllocationBoundary(t, binary, server.URL, false) })
	t.Run("configured-missing-reference", func(t *testing.T) { testCredentialAllocationBoundary(t, binary, server.URL, true) })
}

func testCredentialAllocationBoundary(t *testing.T, binary, endpoint string, missing bool) {
	t.Helper()
	home := t.TempDir()
	configure(t, home, endpoint+"/v1")
	id := "personal"
	if missing {
		id = "openai"
	}
	path := filepath.Join(home, "settings.yaml")
	// #nosec G304 -- Read only this test-created settings fixture.
	settings, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	settings = bytes.ReplaceAll(settings, []byte("env:QUAL_FIXTURE_API_KEY"), []byte("profile:"+id))
	// #nosec G703 -- Fixed settings file under the test-created home.
	if err = os.WriteFile(path, settings, 0o600); err != nil {
		t.Fatal(err)
	}
	if !missing {
		_, diagnostic, e := invoke(t, binary, home, t.TempDir(), "other-provider-credential\n", "auth", "login", "--kind", "anthropic", "--profile", id, "--stdin")
		if e != nil {
			t.Fatalf("saved account fixture: %v %s", e, diagnostic)
		}
	}
	p := startTerminal(t, binary, home, t.TempDir(), 80, "init", "--kind", "openai", "--connection", "new-account", "--endpoint", endpoint+"/v1", "--model", "gpt-6-luna")
	if missing {
		p.expect(t, "Which account?")
		p.send(t, "\x1b[B\r")
	}
	p.expect(t, "API key")
	p.send(t, "fixture-only-credential\r")
	p.expect(t, "Who should verify")
	p.send(t, "\x1b")
	p.wait(t, false)
	verifyOriginalCredential(t, home, id, missing)
}

func verifyOriginalCredential(t *testing.T, home, id string, missing bool) {
	t.Helper()
	// #nosec G304 -- Inspect only the original test-owned credential reference.
	record, err := os.ReadFile(filepath.Join(home, "auth", id+".json"))
	if missing {
		if !os.IsNotExist(err) {
			t.Fatalf("new account rebound missing reference: %v", err)
		}
		return
	}
	if err != nil || !bytes.Contains(record, []byte("other-provider-credential")) || !bytes.Contains(record, []byte("anthropic")) {
		t.Fatalf("another provider's credential was changed: %v", err)
	}
}
