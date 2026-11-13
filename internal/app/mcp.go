package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/iamnalinor/qual/internal/config"
	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/mcp"
)

func (a *App) mcpService(e config.Effective, store *observedStore, id domain.RunID) (*mcp.Service, error) {
	var servers []mcp.ServerConfig
	for _, name := range e.Settings.MCP {
		c := e.Settings.MCPServers[name]
		transport := c.Transport
		if transport == "http" {
			transport = "streamable_http"
		}
		policy := c.Policy
		if policy == "read-only" {
			policy = "read_only"
		}
		server := mcp.ServerConfig{ID: name, Transport: transport, Command: c.Command, URL: c.URL, Credential: c.Credential, Policy: policy, Timeout: time.Duration(c.TimeoutSeconds) * time.Second, MaxOutputBytes: c.MaxOutputBytes}
		if c.Trusted {
			server.ReadOnlyTools = c.ReadOnlyTools
			server.FullTools = c.FullTools
		}
		servers = append(servers, server)
	}
	selected := mcp.Config{Servers: servers, ResolveCredential: func(ctx context.Context, ref string) (mcp.Credential, error) {
		secret, err := a.secret(ctx, ref, "")
		return mcp.Credential{BearerToken: secret}, err
	}}
	if store != nil {
		selected.Audit = func(ctx context.Context, event mcp.AuditEvent) error {
			data, err := json.Marshal(event)
			if err != nil {
				return err
			}
			return store.Append(ctx, domain.Event{SchemaVersion: 1, RunID: id, Type: "mcp_audit", Time: time.Now().UTC(), Data: data})
		}
		selected.PutArtifact = func(ctx context.Context, data []byte) (string, error) {
			artifact, err := store.PutArtifact(ctx, id, "mcp-output", bytes.NewReader(data))
			return artifact.ID, err
		}
	}
	return mcp.New(selected)
}

// MCP probes only handshake, schemas and authorized discovery; it never invokes tools.
func (a *App) MCP(ctx context.Context, target, name string) (retErr error) {
	e, err := a.Config(ctx, target)
	if err != nil {
		return err
	}
	if name != "" {
		if _, ok := e.Settings.MCPServers[name]; !ok {
			return invalid("unknown MCP server")
		}
		e.Settings.MCP = []string{name}
	}
	service, err := a.mcpService(e, nil, "")
	if err != nil {
		return err
	}
	defer func() {
		if err := service.Close(); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()
	infos, err := service.Test(ctx)
	if err != nil {
		return err
	}
	if err = a.selectUI(e); err != nil {
		return err
	}
	return a.renderMCP(infos)
}
