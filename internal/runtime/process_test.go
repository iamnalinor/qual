package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
)

func TestProcessCancellationKillsDescendantsAndCleansEnvironment(t *testing.T) {
	root := t.TempDir()
	call := domain.ToolCall{Name: "shell"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call.Arguments = json.RawMessage(`{"command":"sleep 300 & echo $! > child.pid; wait"}`)
	done := make(chan error, 1)
	go func() { _, e := runCommand(ctx, root, call); done <- e }()
	deadline := time.Now().Add(3 * time.Second)
	var data []byte
	var err error
	for time.Now().Before(deadline) {
		data, err = os.ReadFile(filepath.Join(root, "child.pid")) //nolint:gosec // Fixed helper-process fixture under t.TempDir.
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	childExited := false
	t.Cleanup(func() {
		if !childExited {
			// Also stop the fixture if cancellation or the assertion fails.
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Errorf("stop child fixture: %v", err)
			}
		}
	})
	cancel()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("cancelled command succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("command did not terminate")
	}
	waitForChildExit(t, pid)
	childExited = true
}

func waitForChildExit(t *testing.T, pid int) {
	t.Helper()
	// Cmd.Run waits for the shell, but SIGKILL can still be taking effect in
	// descendants. Linux can also leave an orphan zombie until init reaps it.
	deadline := time.Now().Add(3 * time.Second)
	for {
		stat, readErr := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if errors.Is(readErr, os.ErrNotExist) || (readErr == nil && strings.Contains(string(stat), ") Z ")) {
			return
		}
		if readErr != nil {
			t.Fatalf("read child state: %v", readErr)
		}
		if time.Now().After(deadline) {
			t.Fatalf("child remains alive after cancellation: %s", stat)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProcessCleansEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("QUAL_FIXTURE_SECRET", "must-not-inherit")
	call := domain.ToolCall{Name: "shell", Arguments: json.RawMessage(`{"command":"printf '%s' \"$QUAL_FIXTURE_SECRET\""}`)}
	output, err := runCommand(context.Background(), root, call)
	if err != nil || output != "" {
		t.Fatalf("inherited credential: %q %v", output, err)
	}
}

func TestProcessToolUsesPrivateHomeCacheAndTrustedToolPath(t *testing.T) {
	rt, state, p := requestFixture(t, &pricedProvider{})
	t.Setenv("OPENAI_API_KEY", "fixture-ambient-secret")
	trusted := createSelectedTool(t)
	root, err := exec.CommandContext(t.Context(), "go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatal(err)
	}
	rt.config.ToolPath = trusted + ":" + filepath.Join(strings.TrimSpace(string(root)), "bin") + ":/usr/bin:/bin"
	if err := os.WriteFile(filepath.Join(p.Workspace, "go.mod"), []byte("module fixture.example/process\n\ngo 1.23.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Workspace, "main_test.go"), []byte("package sample\nimport \"testing\"\nfunc TestWorks(t *testing.T){}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	call := domain.ToolCall{ID: "private-env", Name: "shell", Arguments: json.RawMessage(`{"command":"selected-tool && go test ./... && printf 'HOME=%s\\nCACHE=%s\\nTEMP=%s\\n' \"$HOME\" \"$GOCACHE\" \"$TMPDIR\" && env","timeout_seconds":120}`)}
	result := rt.processTool(t.Context(), state.RunID, p.Workspace, call)
	if result.IsError || !strings.Contains(result.Content, "selected-tool-ok") || !strings.Contains(result.Content, "ok ") {
		t.Fatalf("controlled Go command failed: %+v", result)
	}
	if strings.Contains(result.Content, "fixture-ambient-secret") || strings.Contains(result.Content, "OPENAI_API_KEY=") {
		t.Fatal("ambient credential leaked to process")
	}
	assertControlledProcessPaths(t, result.Content, rt.config.Home)
}

func assertControlledProcessPaths(t *testing.T, content, home string) {
	t.Helper()
	for _, marker := range []string{"HOME=", "CACHE=", "TEMP="} {
		start := strings.Index(content, marker)
		if start < 0 {
			t.Fatal("missing controlled path", marker)
		}
		path := strings.SplitN(content[start+len(marker):], "\n", 2)[0]
		if !strings.HasPrefix(path, home+string(filepath.Separator)) {
			t.Fatalf("path outside controlled home: %s", path)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("unusable process directory: %s %v", path, err)
		}
	}
}

func createSelectedTool(t *testing.T) string {
	t.Helper()
	trusted := filepath.Join(t.TempDir(), "trusted-bin")
	if err := os.Mkdir(trusted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trusted, "selected-tool"), []byte("#!/bin/sh\nprintf 'selected-tool-ok\\n'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- Deliberately executable test-owned trusted tool fixture.
	if err := os.Chmod(filepath.Join(trusted, "selected-tool"), 0o700); err != nil {
		t.Fatal(err)
	}
	return trusted
}
