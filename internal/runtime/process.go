package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/report"
)

// cappedOutput bounds process memory while retaining head and tail diagnostics.
type cappedOutput struct {
	mu         sync.Mutex
	head, tail []byte
	total      int64
}

func (b *cappedOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	b.total += int64(n)
	const half = 2 * 1024 * 1024
	remaining := half - len(b.head)
	if remaining > len(data) {
		remaining = len(data)
	}
	b.head = append(b.head, data[:remaining]...)
	data = data[remaining:]
	if len(data) >= half {
		b.tail = append(b.tail[:0], data[len(data)-half:]...)
	} else {
		b.tail = append(b.tail, data...)
		if len(b.tail) > half {
			b.tail = append(b.tail[:0], b.tail[len(b.tail)-half:]...)
		}
	}
	return n, nil
}

func (b *cappedOutput) String() string {
	if b.total > int64(len(b.head)+len(b.tail)) {
		return fmt.Sprintf("%s\n[PROCESS OUTPUT TRUNCATED: %d bytes; output above 4 MiB discarded]\n%s", b.head, b.total, b.tail)
	}
	return string(b.head) + string(b.tail)
}

func commandArgs(call domain.ToolCall) ([]string, time.Duration, error) {
	if call.Name == "git" {
		var a struct {
			Args []string `json:"args"`
		}
		if err := report.Decode(call.Arguments, &a); err != nil {
			return nil, 0, err
		}
		if len(a.Args) == 0 {
			return nil, 0, coded(domain.ErrTool, "git args required", nil)
		}
		return append([]string{"git"}, a.Args...), time.Minute, nil
	}
	var a struct {
		Command string `json:"command"`
		Timeout int    `json:"timeout_seconds"`
	}
	if err := report.Decode(call.Arguments, &a); err != nil {
		return nil, 0, err
	}
	if strings.TrimSpace(a.Command) == "" {
		return nil, 0, coded(domain.ErrTool, "shell command required", nil)
	}
	if a.Timeout <= 0 || a.Timeout > 300 {
		a.Timeout = 60
	}
	return []string{"/bin/sh", "-c", a.Command}, time.Duration(a.Timeout) * time.Second, nil
}

func runCommand(ctx context.Context, workspace string, call domain.ToolCall) (string, error) {
	var output cappedOutput
	err := commandTo(ctx, workspace, call, &output)
	return output.String(), err
}

func commandTo(ctx context.Context, workspace string, call domain.ToolCall, output io.Writer) (err error) {
	home, err := os.MkdirTemp("", "qual-process-home-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(home)) }()
	env, err := processEnvironment(home, "")
	if err != nil {
		return err
	}
	return commandWithEnvironment(ctx, workspace, call, output, env)
}

func commandWithEnvironment(ctx context.Context, workspace string, call domain.ToolCall, output io.Writer, env []string) error {
	args, timeout, err := commandArgs(call)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	program, err := processExecutable(args[0], env)
	if err != nil {
		return coded(domain.ErrTool, "command unavailable", err)
	}
	cmd := exec.CommandContext(ctx, program, args[1:]...) //nolint:gosec // Explicit shell/git capability; clean environment, workspace and deadline follow.
	cmd.Dir = workspace
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = time.Second
	cmd.Stdout = output
	cmd.Stderr = output
	if err = cmd.Run(); err != nil {
		return coded(domain.ErrTool, "command failed", err)
	}
	return nil
}

func (r *Runtime) processTool(ctx context.Context, id domain.RunID, workspace string, call domain.ToolCall) domain.ToolResult {
	result := domain.ToolResult{CallID: call.ID}
	env, err := r.roleProcessEnvironment(workspace)
	if err != nil {
		result.IsError = true
		result.Content = err.Error()
		return result
	}
	file, err := os.CreateTemp("", "qual-process-")
	if err != nil {
		result.IsError = true
		result.Content = err.Error()
		return result
	}
	path := file.Name()
	commandErr := commandWithEnvironment(ctx, workspace, call, file, env)
	result, saveErr := r.preserveProcess(context.WithoutCancel(ctx), id, call.ID, file)
	closeErr := file.Close()
	removeErr := os.Remove(path)
	if err = errors.Join(commandErr, saveErr, closeErr, removeErr); err != nil {
		result.IsError = true
		result.Content += "\n" + err.Error()
	}
	return result
}

func (r *Runtime) preserveProcess(ctx context.Context, id domain.RunID, callID string, file *os.File) (domain.ToolResult, error) {
	result := domain.ToolResult{CallID: callID}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return result, fmt.Errorf("rewind process output: %w", err)
	}
	artifact, err := r.config.Store.PutArtifact(ctx, id, "process-output", file)
	if err != nil {
		return result, err
	}
	result.ArtifactID = artifact.ID
	limit := r.config.MaxOutputBytes
	if artifact.Size <= int64(limit) {
		result.Content, err = readRange(file.Name(), 0, int64(limit))
		return result, err
	}
	result.Truncated = true
	half := limit / 2
	head := make([]byte, half)
	tail := make([]byte, half)
	if _, err = file.ReadAt(head, 0); err != nil {
		return result, fmt.Errorf("read process head: %w", err)
	}
	if _, err = file.ReadAt(tail, artifact.Size-int64(half)); err != nil {
		return result, fmt.Errorf("read process tail: %w", err)
	}
	result.Content = fmt.Sprintf("%s\n[TRUNCATED %d bytes; full artifact %s; use read_artifact with offset/limit]\n%s", head, artifact.Size, artifact.ID, tail)
	return result, nil
}
