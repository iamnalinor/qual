package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixture(t *testing.T, count *atomic.Int32) *httptest.Server {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
	for _, name := range []string{"read", "write", "hint"} {
		server.AddTool(&sdk.Tool{Name: name, InputSchema: map[string]any{"type": "object"}, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			count.Add(1)
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: strings.Repeat("data", 100)}}}, nil
		})
	}
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	return httptest.NewServer(handler)
}

func TestHTTPDiscoveryPolicyAndArtifacts(t *testing.T) {
	var calls atomic.Int32
	server := fixture(t, &calls)
	defer server.Close()
	var full []byte
	service, err := New(Config{Servers: []ServerConfig{{ID: "local", Transport: "streamable_http", URL: server.URL, ReadOnlyTools: []string{"read"}, FullTools: []string{"write"}, Policy: "full", MaxOutputBytes: 32}}, PutArtifact: func(_ context.Context, b []byte) (string, error) {
		full = append([]byte(nil), b...)
		return "artifact", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	}()
	infos, err := service.Test(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || len(infos[0].Tools) != 2 || calls.Load() != 0 {
		t.Fatalf("probe: %+v calls=%d", infos, calls.Load())
	}
	tools := service.Tools()
	if len(tools) != 2 {
		t.Fatalf("tools: %d", len(tools))
	}
	for _, tool := range tools {
		got, err := tool.Execute(t.Context(), domain.ToolCall{ID: "call", Name: tool.Definition().Name, Arguments: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		assertArtifact(t, got, full)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls %d", calls.Load())
	}
}

func assertArtifact(t *testing.T, got domain.ToolResult, full []byte) {
	t.Helper()
	if !got.IsError || !got.Truncated || got.ArtifactID != "artifact" {
		t.Fatalf("result %+v", got)
	}
	if len(got.Content) > 32 || len(full) <= 32 {
		t.Fatalf("output lengths %d %d", len(got.Content), len(full))
	}
}

func TestDefaultDenyIgnoresReadOnlyHints(t *testing.T) {
	var calls atomic.Int32
	server := fixture(t, &calls)
	defer server.Close()
	service, err := New(Config{Servers: []ServerConfig{{ID: "deny", Transport: "streamable_http", URL: server.URL, FullTools: []string{"write"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	if _, err := service.Test(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(service.Tools()) != 0 || calls.Load() != 0 {
		t.Fatal("annotation or inactive full grant authorized a tool")
	}
}

func closeService(t *testing.T, service *Service) {
	t.Helper()
	if err := service.Close(); err != nil {
		t.Error(err)
	}
}

func TestHTTPAuthAndRedirect(t *testing.T) {
	var calls atomic.Int32
	backend := fixture(t, &calls)
	defer backend.Close()
	token := t.Name()
	var leaks atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaks.Add(1)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer target.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer gateway.Close()
	service, err := New(Config{Servers: []ServerConfig{{ID: "auth", Transport: "streamable_http", URL: gateway.URL, Credential: "reference"}}, ResolveCredential: func(_ context.Context, ref string) (Credential, error) {
		if ref != "reference" {
			t.Error(ref)
		}
		return Credential{BearerToken: token}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	if err := service.Connect(t.Context()); err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("unsafe handshake error %v", err)
	}
	if leaks.Load() != 0 {
		t.Fatal("redirect leaked credential")
	}
}

func TestMutatingTimeoutIsUnknownAndNeverRetried(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	server := sdk.NewServer(&sdk.Implementation{Name: "slow", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "write", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		select {
		case <-ctx.Done():
		case <-release:
		}
		return nil, ctx.Err()
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	var events []AuditEvent
	service, err := New(Config{Servers: []ServerConfig{{ID: "slow", Transport: "streamable_http", URL: httpServer.URL, Policy: "full", FullTools: []string{"write"}, Timeout: 100 * time.Millisecond}}, Audit: func(_ context.Context, event AuditEvent) error { events = append(events, event); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	defer close(release)
	if err := service.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tool := service.Tools()[0]
	_, err = tool.Execute(t.Context(), domain.ToolCall{ID: "uncertain", Name: tool.Definition().Name, Arguments: []byte(`{}`)})
	var coded *domain.Error
	if !errors.As(err, &coded) || coded.Code != "mcp_unknown_outcome" || coded.Retryable {
		t.Fatalf("error %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout cause lost: %v", err)
	}
	assertUnknownAudit(t, calls.Load(), events)
}

func assertUnknownAudit(t *testing.T, calls int32, events []AuditEvent) {
	t.Helper()
	if calls != 1 || len(events) != 2 {
		t.Fatalf("calls %d audit %+v", calls, events)
	}
	if events[0].Phase != "intent" || events[1].Outcome != "unknown" {
		t.Fatalf("audit %+v", events)
	}
}

func TestStdioFixtureProcess(t *testing.T) {
	if os.Getenv("QUAL_MCP_FIXTURE") == "1" {
		startDescendant(t)
		server := sdk.NewServer(&sdk.Implementation{Name: "stdio", Version: "1"}, nil)
		server.AddTool(&sdk.Tool{Name: "env", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: os.Getenv("QUAL_AMBIENT_SECRET") + ":" + os.Getenv("QUAL_GRANTED")}}}, nil
		})
		if err := server.Run(t.Context(), &sdk.StdioTransport{}); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	t.Setenv("QUAL_AMBIENT_SECRET", "must-not-inherit")
	pidPath := filepath.Join(t.TempDir(), "descendant.pid")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Config{Servers: []ServerConfig{{ID: "stdio", Transport: "stdio", Command: []string{executable, "-test.run=^TestStdioFixtureProcess$"}, Credential: t.Name(), ReadOnlyTools: []string{"env"}}}, ResolveCredential: func(context.Context, string) (Credential, error) {
		return Credential{Env: map[string]string{"QUAL_MCP_FIXTURE": "1", "QUAL_GRANTED": "explicit", "QUAL_PID_PATH": pidPath}}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tool := service.Tools()[0]
	result, err := tool.Execute(t.Context(), domain.ToolCall{Name: tool.Definition().Name, Arguments: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Content, "must-not-inherit") || !strings.Contains(result.Content, ":explicit") {
		t.Fatalf("environment %s", result.Content)
	}
	closeService(t, service)
	assertDescendantStopped(t, pidPath)
	if _, err := tool.Execute(t.Context(), domain.ToolCall{Name: tool.Definition().Name, Arguments: []byte(`{}`)}); err == nil {
		t.Fatal("closed process accepted call")
	}
}

func TestCancellationBeforeDispatchHasNoEffect(t *testing.T) {
	var calls atomic.Int32
	server := fixture(t, &calls)
	defer server.Close()
	service, err := New(Config{Servers: []ServerConfig{{ID: "cancel", Transport: "streamable_http", URL: server.URL, Policy: "full", FullTools: []string{"write"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	if err := service.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tool := service.Tools()[0]
	_, err = tool.Execute(ctx, domain.ToolCall{Name: tool.Definition().Name, Arguments: []byte(`{}`)})
	if domain.CodeOf(err) != domain.ErrCancelled || calls.Load() != 0 {
		t.Fatalf("cancellation err=%v calls=%d", err, calls.Load())
	}
}

func TestConfigurationSnapshotCannotGainAuthority(t *testing.T) {
	var calls atomic.Int32
	server := fixture(t, &calls)
	defer server.Close()
	servers := []ServerConfig{{ID: "snapshot", Transport: "streamable_http", URL: server.URL, ReadOnlyTools: []string{"read"}}}
	service, err := New(Config{Servers: servers})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	servers[0].ReadOnlyTools[0] = "write"
	if err := service.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	if service.Tools()[0].Definition().Name != toolName("snapshot", "read") {
		t.Fatal("caller mutation expanded authority")
	}
}

func TestHTTPAuthenticatedDiscoveryAndCalls(t *testing.T) {
	var calls atomic.Int32
	server := sdk.NewServer(&sdk.Implementation{Name: "authorized", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		calls.Add(1)
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "authorized"}}}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	token := t.Name()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	service, err := New(Config{Servers: []ServerConfig{{ID: "authorized", Transport: "streamable_http", URL: httpServer.URL, Credential: t.Name(), ReadOnlyTools: []string{"read"}}}, ResolveCredential: func(context.Context, string) (Credential, error) { return Credential{BearerToken: token}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	if err := service.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tool := service.Tools()[0]
	if strings.Contains(tool.Definition().Description, token) {
		t.Fatal("credential in definition")
	}
	result, err := tool.Execute(t.Context(), domain.ToolCall{Name: tool.Definition().Name, Arguments: []byte(`{}`)})
	if err != nil || !strings.Contains(result.Content, "authorized") || calls.Load() != 1 {
		t.Fatalf("result %+v err %v calls %d", result, err, calls.Load())
	}
}

func TestInvalidArgumentsNeverReachServer(t *testing.T) {
	var calls atomic.Int32
	server := fixture(t, &calls)
	defer server.Close()
	service, err := New(Config{Servers: []ServerConfig{{ID: "args", Transport: "streamable_http", URL: server.URL, ReadOnlyTools: []string{"read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	if err := service.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tool := service.Tools()[0]
	for _, args := range []string{`null`, `[]`, `{"a":`, strings.Repeat(" ", 17<<20)} {
		if _, err := tool.Execute(t.Context(), domain.ToolCall{Name: tool.Definition().Name, Arguments: []byte(args)}); err == nil {
			t.Fatalf("accepted invalid arguments")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid call reached server")
	}
}

func TestSchemaValidationRejectsInvalidDefinitions(t *testing.T) {
	for _, schema := range []any{map[string]any{"type": "string"}, map[string]any{"type": "object", "properties": map[string]any{"bad": map[string]any{"type": "invalid"}}}, nil} {
		if _, _, err := resolveSchema(schema); err == nil {
			t.Fatalf("accepted schema %v", schema)
		}
	}
}

func TestResponseBoundIsAnExplicitError(t *testing.T) {
	body := &boundedBody{ReadCloser: io.NopCloser(strings.NewReader("12345")), remaining: 4}
	got, err := io.ReadAll(body)
	if err == nil || string(got) != "1234" {
		t.Fatalf("response %q error %v", got, err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMissingArtifactSinkDoesNotSilentlyDiscardOutput(t *testing.T) {
	var calls atomic.Int32
	server := fixture(t, &calls)
	defer server.Close()
	service, err := New(Config{Servers: []ServerConfig{{ID: "bound", Transport: "streamable_http", URL: server.URL, ReadOnlyTools: []string{"read"}, MaxOutputBytes: 16}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	if err := service.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tool := service.Tools()[0]
	result, err := tool.Execute(t.Context(), domain.ToolCall{Name: tool.Definition().Name, Arguments: []byte(`{}`)})
	if err == nil {
		t.Fatal("discarded output without artifact")
	}
	if len(result.Content) > 16 {
		t.Fatal("error result exceeded output cap")
	}
}

func startDescendant(t *testing.T) {
	t.Helper()
	child := exec.CommandContext(t.Context(), "/bin/sleep", "1000")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G703: parent test supplies its own t.TempDir path to the isolated fixture.
	if err := os.WriteFile(os.Getenv("QUAL_PID_PATH"), []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertDescendantStopped(t *testing.T, pidPath string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		return
	}
	//nolint:gosec // G304: pidPath is generated by this test under t.TempDir.
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	// A killed orphan may await its container init's reaper. It cannot execute.
	raw, err = os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 3 || fields[2] != "Z" {
		t.Fatalf("owned descendant still running: %s", raw)
	}
}

func TestMalformedHTTPToolResultReturnsSafeError(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "malformed", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			handler.ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if err := r.Body.Close(); err != nil {
			t.Error(err)
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var envelope struct {
			Method string          `json:"method"`
			ID     json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Error(err)
			return
		}
		if envelope.Method != "tools/call" {
			handler.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(envelope.ID) + `,"result":{"content":"malformed-sensitive-data"}}`))
		if err != nil {
			t.Error(err)
		}
	}))
	defer httpServer.Close()
	service, err := New(Config{Servers: []ServerConfig{{ID: "malformed", Transport: "streamable_http", URL: httpServer.URL, ReadOnlyTools: []string{"read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	if err := service.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tool := service.Tools()[0]
	_, err = tool.Execute(t.Context(), domain.ToolCall{Name: tool.Definition().Name, Arguments: []byte(`{}`)})
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe malformed result error %v", err)
	}
}

func TestHTTPResponseLimitRejectsOversizedResult(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "oversized", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: strings.Repeat("large", 4096)}}}, nil
	})
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	service, err := New(Config{Servers: []ServerConfig{{ID: "oversized", Transport: "streamable_http", URL: httpServer.URL, ReadOnlyTools: []string{"read"}, MaxResponseBytes: 4096, MaxOutputBytes: 128}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	if err := service.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tool := service.Tools()[0]
	result, err := tool.Execute(t.Context(), domain.ToolCall{Name: tool.Definition().Name, Arguments: []byte(`{}`)})
	if err == nil || len(result.Content) > 128 {
		t.Fatalf("oversized response result %+v err %v", result, err)
	}
}

func TestAuditFailurePreventsDispatch(t *testing.T) {
	var calls atomic.Int32
	server := fixture(t, &calls)
	defer server.Close()
	service, err := New(Config{Servers: []ServerConfig{{ID: "audit", Transport: "streamable_http", URL: server.URL, ReadOnlyTools: []string{"read"}}}, Audit: func(context.Context, AuditEvent) error { return errors.New("fixture persistence failure") }})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, service)
	if err := service.Connect(t.Context()); err != nil {
		t.Fatal(err)
	}
	tool := service.Tools()[0]
	if _, err := tool.Execute(t.Context(), domain.ToolCall{Name: tool.Definition().Name, Arguments: []byte(`{}`)}); err == nil {
		t.Fatal("ignored audit failure")
	}
	if calls.Load() != 0 {
		t.Fatal("dispatched without persisted intent")
	}
}

func TestNamesAreBoundedStableAndDistinct(t *testing.T) {
	server := strings.Repeat("server/", 100)
	name := strings.Repeat("tool-", 100)
	first := toolName(server, name)
	if first != toolName(server, name) || len(first) > 64 {
		t.Fatalf("unstable or oversized name %q", first)
	}
	if first == toolName(server, name+"more") {
		t.Fatal("bounded prefixes collided")
	}
	if toolName("a-b", "read") == toolName("a_b", "read") {
		t.Fatal("sanitized namespace collision")
	}
}
