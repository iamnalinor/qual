package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *Service) transport(ctx context.Context, server ServerConfig, credential Credential) (sdk.Transport, func() error, error) {
	if server.Transport == "stdio" {
		return stdio(ctx, server, credential)
	}
	client := http.Client{Timeout: server.Timeout}
	if s.config.HTTPClient != nil {
		client = *s.config.HTTPClient
		client.Timeout = server.Timeout
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = &authTransport{base: base, token: credential.BearerToken, limit: int64(server.MaxResponseBytes)}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &sdk.StreamableClientTransport{Endpoint: server.URL, HTTPClient: &client, MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: server.MaxResponseBytes}, func() error { return nil }, nil
}

func stdio(ctx context.Context, server ServerConfig, credential Credential) (sdk.Transport, func() error, error) {
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	// Trusted user configuration is the only source of executable and arguments.
	//nolint:gosec // G204: MCP explicitly executes trusted administrator commands.
	cmd := exec.CommandContext(life, server.Command[0], server.Command[1:]...)
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin"}
	for key, value := range credential.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			cancel()
			return nil, nil, failure(domain.ErrInvalidConfig, "invalid MCP environment grant", nil)
		}
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	reader, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, failure(domain.ErrTool, "MCP stdout pipe", err)
	}
	writer, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, nil, errors.Join(failure(domain.ErrTool, "MCP stdin pipe", err), reader.Close())
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, nil, errors.Join(failure(domain.ErrTool, "MCP process start", err), reader.Close(), writer.Close())
	}
	stop := func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cancel()
		// Wait reaps the process; termination status is expected during shutdown.
		waitErr := cmd.Wait()
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			waitErr = nil
		}
		if errors.Is(err, syscall.ESRCH) {
			err = nil
		}
		return errors.Join(err, waitErr)
	}
	return &sdk.IOTransport{Reader: &processReader{reader}, Writer: &processWriter{writer}, MaxLineLength: server.MaxResponseBytes}, stop, nil
}

type authTransport struct {
	base  http.RoundTripper
	token string
	limit int64
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if t.token != "" {
		clone.Header.Set("Authorization", "Bearer "+t.token)
	}
	response, err := t.base.RoundTrip(clone)
	if err != nil {
		return nil, err
	}
	response.Body = &boundedBody{ReadCloser: response.Body, remaining: t.limit}
	return response, nil
}

type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, failure(domain.ErrTool, "MCP response exceeds limit", nil)
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

// exec.Wait also closes its pipes; an already closed owned pipe is a successful shutdown.
type processReader struct{ io.ReadCloser }

func (p *processReader) Close() error { return closePipe(p.ReadCloser) }

type processWriter struct{ io.WriteCloser }

func (p *processWriter) Close() error { return closePipe(p.WriteCloser) }
func closePipe(pipe io.Closer) error {
	err := pipe.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}
