// Package mcp adapts trusted MCP connections to runtime tools.
package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/port"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerConfig is supplied exclusively by trusted application configuration.
type ServerConfig struct {
	ID, Transport                    string
	Command                          []string
	URL, Credential, Policy          string
	ReadOnlyTools, FullTools         []string
	Timeout                          time.Duration
	MaxOutputBytes, MaxResponseBytes int
}

// Credential contains explicitly granted transport credentials, never definitions.
type Credential struct {
	BearerToken string
	Env         map[string]string
}

// AuditEvent records intent before dispatch and outcome after dispatch. Arguments are omitted.
type AuditEvent struct {
	ServerID, Tool, CallID, Phase, Outcome string
	Duration                               time.Duration
}

// Config supplies trusted servers and persistence hooks. Audit must durably save intent.
type Config struct {
	Servers           []ServerConfig
	ResolveCredential func(context.Context, string) (Credential, error)
	Audit             func(context.Context, AuditEvent) error
	PutArtifact       func(context.Context, []byte) (string, error)
	HTTPClient        *http.Client
}

// ServerInfo describes a successful read-only discovery probe.
type ServerInfo struct {
	ID    string
	Tools []domain.ToolDefinition
}

// Service owns MCP sessions and their subprocesses. Connect and Close serialize lifecycle changes.
type Service struct {
	mu          sync.Mutex
	config      Config
	connections []*connection
	tools       []port.Tool
	closed      bool
}

type connection struct {
	config  ServerConfig
	session *sdk.ClientSession
	stop    func() error
}

// New validates trusted connection configuration without performing external effects.
func New(config Config) (*Service, error) {
	config.Servers = slices.Clone(config.Servers)
	seen := map[string]bool{}
	for i := range config.Servers {
		server := &config.Servers[i]
		server.Command = slices.Clone(server.Command)
		server.ReadOnlyTools = slices.Clone(server.ReadOnlyTools)
		server.FullTools = slices.Clone(server.FullTools)
		if server.ID == "" || seen[server.ID] {
			return nil, failure(domain.ErrInvalidConfig, "duplicate or empty MCP server ID", nil)
		}
		seen[server.ID] = true
		if err := validateServer(*server); err != nil {
			return nil, err
		}
		if server.Timeout == 0 {
			server.Timeout = 30 * time.Second
		}
		if server.MaxOutputBytes == 0 {
			server.MaxOutputBytes = 64 << 10
		}
		if server.MaxResponseBytes == 0 {
			server.MaxResponseBytes = 16 << 20
		}
		if server.Timeout < 0 || server.MaxOutputBytes < 1 || server.MaxResponseBytes < server.MaxOutputBytes {
			return nil, failure(domain.ErrInvalidConfig, "invalid MCP limits", nil)
		}
	}
	return &Service{config: config}, nil
}

func validateServer(server ServerConfig) error {
	if server.Policy != "" && server.Policy != "read_only" && server.Policy != "full" {
		return failure(domain.ErrInvalidConfig, "invalid MCP policy", nil)
	}
	switch server.Transport {
	case "stdio":
		if len(server.Command) == 0 || server.Command[0] == "" || server.URL != "" {
			return failure(domain.ErrInvalidConfig, "invalid MCP stdio command", nil)
		}
	case "streamable_http":
		return validateHTTP(server)
	default:
		return failure(domain.ErrInvalidConfig, "unsupported MCP transport", nil)
	}
	return nil
}

func validateHTTP(server ServerConfig) error {
	u, err := url.Parse(server.URL)
	if err != nil {
		return failure(domain.ErrInvalidConfig, "invalid MCP HTTP endpoint", nil)
	}
	if u.Host == "" || u.User != nil || u.Fragment != "" || len(server.Command) != 0 {
		return failure(domain.ErrInvalidConfig, "invalid MCP HTTP endpoint", nil)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return failure(domain.ErrInvalidConfig, "invalid MCP HTTP scheme", nil)
	}
	return nil
}

// Connect initializes sessions and discovers tools atomically, closing all resources on failure.
func (s *Service) Connect(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return failure(domain.ErrTool, "MCP service is closed", nil)
	}
	if len(s.connections) > 0 {
		return nil
	}
	for _, server := range s.config.Servers {
		if err := s.connectServer(ctx, server); err != nil {
			cleanup := s.closeConnections()
			s.tools = nil
			return errors.Join(err, cleanup)
		}
	}
	return nil
}

func (s *Service) connectServer(ctx context.Context, server ServerConfig) error {
	ctx, cancel := context.WithTimeout(ctx, server.Timeout)
	defer cancel()
	credential, err := s.resolve(ctx, server.Credential)
	if err != nil {
		return err
	}
	transport, stop, err := s.transport(ctx, server, credential)
	if err != nil {
		return err
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "qual", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return errors.Join(failure(domain.ErrTool, "MCP handshake failed", safeTransportError{err}), stop())
	}
	conn := &connection{config: server, session: session, stop: stop}
	s.connections = append(s.connections, conn)
	return s.discover(ctx, conn)
}

func (s *Service) resolve(ctx context.Context, ref string) (Credential, error) {
	if ref == "" {
		return Credential{}, nil
	}
	if s.config.ResolveCredential == nil {
		return Credential{}, failure(domain.ErrAuth, "MCP credential resolver unavailable", nil)
	}
	credential, err := s.config.ResolveCredential(ctx, ref)
	if err != nil {
		return Credential{}, failure(domain.ErrAuth, "MCP credential resolution failed", safeTransportError{err})
	}
	return credential, nil
}

// Tools returns discovered authorized capabilities, in configured server/discovery order.
func (s *Service) Tools() []port.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]port.Tool(nil), s.tools...)
}

// List performs only handshake and discovery, never tool calls.
func (s *Service) List(ctx context.Context) ([]ServerInfo, error) {
	if err := s.Connect(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	infos := make([]ServerInfo, 0, len(s.connections))
	for _, conn := range s.connections {
		info := ServerInfo{ID: conn.config.ID}
		for _, tool := range s.tools {
			t, ok := tool.(*remoteTool)
			if ok && t.conn == conn {
				info.Tools = append(info.Tools, t.Definition())
			}
		}
		infos = append(infos, info)
	}
	return infos, nil
}

// Test validates connection and discovered schemas without invoking tools.
func (s *Service) Test(ctx context.Context) ([]ServerInfo, error) { return s.List(ctx) }

// Close closes HTTP sessions and kills all owned stdio process groups.
func (s *Service) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.closeConnections()
}

func (s *Service) closeConnections() error {
	var result error
	for _, conn := range s.connections {
		result = errors.Join(result, conn.stop(), conn.session.Close())
	}
	s.connections = nil
	return result
}

func failure(code domain.ErrorCode, message string, cause error) error {
	return &domain.Error{Code: code, Message: message, Cause: cause}
}

// safeTransportError retains cancellation identity without exposing server error text.
type safeTransportError struct{ cause error }

func (e safeTransportError) Error() string { return "MCP transport failure" }
func (e safeTransportError) Unwrap() error { return e.cause }
