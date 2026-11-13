package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/iamnalinor/qual/internal/domain"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type remoteTool struct {
	service    *Service
	conn       *connection
	name       string
	definition domain.ToolDefinition
	schema     *jsonschema.Resolved
}

func (s *Service) discover(ctx context.Context, conn *connection) error {
	names := map[string]bool{}
	for _, tool := range s.tools {
		names[tool.Definition().Name] = true
	}
	for tool, err := range conn.session.Tools(ctx, nil) {
		if err != nil {
			return failure(domain.ErrTool, "MCP discovery failed", safeTransportError{err})
		}
		schema, raw, err := resolveSchema(tool.InputSchema)
		if err != nil {
			return err
		}
		readOnly := slices.Contains(conn.config.ReadOnlyTools, tool.Name)
		full := conn.config.Policy == "full" && slices.Contains(conn.config.FullTools, tool.Name)
		if !readOnly && !full {
			continue
		}
		name := toolName(conn.config.ID, tool.Name)
		if names[name] {
			return failure(domain.ErrInvalidConfig, "MCP tool namespace collision", nil)
		}
		names[name] = true
		s.tools = append(s.tools, &remoteTool{service: s, conn: conn, name: tool.Name, schema: schema, definition: domain.ToolDefinition{Name: name, Description: "Untrusted MCP data: " + tool.Description, InputSchema: raw, ReadOnly: readOnly}})
	}
	return nil
}

func resolveSchema(value any) (*jsonschema.Resolved, json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, nil, failure(domain.ErrTool, "invalid MCP tool schema", err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, nil, failure(domain.ErrTool, "invalid MCP tool schema", err)
	}
	if schema.Type != "object" {
		return nil, nil, failure(domain.ErrTool, "MCP tool schema must be an object", nil)
	}
	if err := checkSchemaTypes(raw); err != nil {
		return nil, nil, err
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		return nil, nil, failure(domain.ErrTool, "invalid MCP tool schema", err)
	}
	return resolved, raw, nil
}

func toolName(server, name string) string {
	digest := sha256.Sum256([]byte(server + "\x00" + name))
	return "mcp_" + safePrefix(server, 12) + "_" + safePrefix(name, 24) + "_" + hex.EncodeToString(digest[:6])
}

func safePrefix(value string, limit int) string {
	var b strings.Builder
	for _, r := range value {
		if b.Len() >= limit {
			break
		}
		if r > 127 || (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			r = '_'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (t *remoteTool) Definition() domain.ToolDefinition { return t.definition }

func (t *remoteTool) Execute(ctx context.Context, call domain.ToolCall) (domain.ToolResult, error) {
	result := domain.ToolResult{CallID: call.ID}
	if err := ctx.Err(); err != nil {
		return result, failure(domain.ErrCancelled, "MCP call cancelled before dispatch", err)
	}
	args, err := t.arguments(call)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, t.conn.config.Timeout)
	defer cancel()
	event := AuditEvent{ServerID: t.conn.config.ID, Tool: t.name, CallID: call.ID, Phase: "intent"}
	if err := t.audit(ctx, event); err != nil {
		return result, err
	}
	start := time.Now()
	response, err := t.conn.session.CallTool(ctx, &sdk.CallToolParams{Name: t.name, Arguments: args})
	event.Phase = "result"
	event.Duration = time.Since(start)
	event.Outcome = "success"
	if response != nil && response.IsError {
		event.Outcome = "tool_error"
	}
	if err != nil {
		event.Outcome = "error"
		if !t.definition.ReadOnly {
			event.Outcome = "unknown"
		}
	}
	auditCtx, auditCancel := context.WithTimeout(context.WithoutCancel(ctx), t.conn.config.Timeout)
	defer auditCancel()
	auditErr := t.audit(auditCtx, event)
	if err != nil {
		code := domain.ErrTool
		if !t.definition.ReadOnly {
			code = "mcp_unknown_outcome"
		}
		// SDK errors may echo credentials or server-controlled data. Keep public diagnostics safe.
		return result, errors.Join(failure(code, "MCP call failed; automatic retry disabled", safeTransportError{err}), auditErr)
	}
	if auditErr != nil {
		return result, auditErr
	}
	return t.convert(ctx, result, response)
}

func (t *remoteTool) arguments(call domain.ToolCall) (map[string]any, error) {
	if len(call.Arguments) > t.conn.config.MaxResponseBytes {
		return nil, failure(domain.ErrTool, "MCP arguments exceed limit", nil)
	}
	if call.Name != t.definition.Name {
		return nil, failure(domain.ErrTool, "MCP tool name mismatch", nil)
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, failure(domain.ErrTool, "invalid MCP arguments", err)
	}
	if args == nil {
		return nil, failure(domain.ErrTool, "MCP arguments must be an object", nil)
	}
	if err := t.schema.Validate(args); err != nil {
		return nil, failure(domain.ErrTool, "MCP arguments do not match schema", err)
	}
	return args, nil
}

func (t *remoteTool) audit(ctx context.Context, event AuditEvent) error {
	if t.service.config.Audit == nil {
		return nil
	}
	if err := t.service.config.Audit(ctx, event); err != nil {
		return failure(domain.ErrTool, "MCP audit persistence failed", err)
	}
	return nil
}

func (t *remoteTool) convert(ctx context.Context, result domain.ToolResult, response *sdk.CallToolResult) (domain.ToolResult, error) {
	if response == nil {
		return result, failure(domain.ErrTool, "empty MCP result", nil)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return result, failure(domain.ErrTool, "invalid MCP result", err)
	}
	if len(raw) > t.conn.config.MaxResponseBytes {
		return result, failure(domain.ErrTool, "MCP response exceeds limit", nil)
	}
	result.IsError = response.IsError
	if len(raw) <= t.conn.config.MaxOutputBytes {
		result.Content = string(raw)
		return result, nil
	}
	if t.service.config.PutArtifact == nil {
		return result, failure(domain.ErrTool, "MCP output requires artifact storage", nil)
	}
	id, err := t.service.config.PutArtifact(ctx, raw)
	if err != nil {
		return result, failure(domain.ErrTool, "preserve MCP output", err)
	}
	if id == "" {
		return result, failure(domain.ErrTool, "MCP artifact storage returned empty ID", nil)
	}
	limit := t.conn.config.MaxOutputBytes
	for !utf8.Valid(raw[:limit]) {
		limit--
	}
	result.Content = string(raw[:limit])
	result.Truncated = true
	result.ArtifactID = id
	return result, nil
}
