package config

import (
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/iamnalinor/qual/internal/budget"
	"github.com/iamnalinor/qual/internal/domain"
)

var (
	envRef     = regexp.MustCompile(`^env:[A-Za-z_][A-Za-z0-9_]*$`)
	profileRef = regexp.MustCompile(`^profile:[A-Za-z0-9_.-]+$`)
)

func credentialValid(s string) bool {
	return s == "" || envRef.MatchString(s) || profileRef.MatchString(s)
}

func safeName(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00")
}

func safePath(s string) bool {
	return s != "" && s != "." && !strings.ContainsAny(s, "\\\x00") && !path.IsAbs(s) && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../")
}

// Validate checks an effective configuration. Empty role configuration is allowed for init/config commands.
func Validate(s Settings) error {
	if s.SchemaVersion != 1 {
		return invalid("unsupported settings schema version")
	}
	if s.Trust != "untrusted" && s.Trust != "trusted" {
		return invalid("invalid trust mode")
	}
	for name, c := range s.Connections {
		if err := validateConnection(name, c); err != nil {
			return err
		}
	}
	if err := validateMCP(s); err != nil {
		return err
	}
	for _, file := range s.InstructionFiles {
		if !safePath(file.Path) {
			return invalid("unsafe instruction path")
		}
		if _, err := path.Match(file.Glob, "file"); err != nil {
			return invalid("invalid instruction glob")
		}
	}
	_, err := resolveAgents(s)
	return err
}

func validateConnection(name string, c Connection) error {
	if !safeName(name) || !credentialValid(c.Credential) {
		return invalid("invalid connection or credential reference")
	}
	switch c.Kind {
	case "openai", "chatgpt", "anthropic", "gemini", "openrouter", "compatible", "openai-compatible", "codex":
	default:
		return invalid("invalid provider kind")
	}
	if c.Endpoint != "" {
		if err := validateURL(c.Endpoint); err != nil {
			return err
		}
	}
	for _, v := range c.Capabilities {
		switch v {
		case "tools", "streaming", "reasoning":
		default:
			return invalid("invalid connection capability")
		}
	}
	return nil
}

func validateURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return invalid("invalid endpoint; credentials must be references")
	}
	return nil
}

func validateMCP(s Settings) error {
	for name, m := range s.MCPServers {
		if err := validateMCPServer(name, m); err != nil {
			return err
		}
	}
	for _, name := range s.MCP {
		if _, ok := s.MCPServers[name]; !ok {
			return invalid("selected MCP server is undefined")
		}
	}
	return nil
}

func validateMCPServer(name string, m MCPServer) error {
	if !safeName(name) || !credentialValid(m.Credential) {
		return invalid("invalid MCP connection")
	}
	if err := validateMCPGrants(m); err != nil {
		return err
	}
	if m.Policy != "read-only" && m.Policy != "full" {
		return invalid("invalid MCP policy")
	}
	switch m.Transport {
	case "stdio":
		if len(m.Command) == 0 || m.URL != "" {
			return invalid("invalid stdio MCP server")
		}
	case "http":
		if len(m.Command) != 0 {
			return invalid("invalid remote MCP server")
		}
		return validateURL(m.URL)
	default:
		return invalid("invalid MCP transport")
	}
	return nil
}

func resolveAgents(s Settings) ([]domain.AgentSpec, error) {
	result := []domain.AgentSpec{}
	seen := map[string]bool{}
	for _, a := range s.Reviewers {
		spec, err := agentSpec(s, a, domain.RoleReviewer)
		if err != nil {
			return nil, err
		}
		if seen[a.ID] {
			return nil, invalid("duplicate agent ID")
		}
		seen[a.ID] = true
		result = append(result, spec)
	}
	if s.Leader.ID != "" {
		spec, err := agentSpec(s, s.Leader, domain.RoleLeader)
		if err != nil {
			return nil, err
		}
		if seen[s.Leader.ID] {
			return nil, invalid("duplicate agent ID")
		}
		result = append(result, spec)
	} else if len(s.Reviewers) > 0 {
		return nil, invalid("reviewers require exactly one leader")
	}
	if s.Setup.ID != "" {
		if _, err := agentSpec(s, s.Setup, domain.RoleSetup); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func agentSpec(s Settings, a Agent, role domain.Role) (domain.AgentSpec, error) {
	spec := domain.AgentSpec{ID: domain.AgentID(a.ID), Role: role, Provider: a.Provider, Model: a.Model, ReasoningEffort: a.Reasoning, InvestigationEffort: a.Investigation, Focus: a.Focus, Instructions: a.Instructions, Language: a.Language}
	if !safeName(a.ID) || a.Model == "" {
		return spec, invalid("agent requires valid ID and model")
	}
	if _, ok := s.Connections[a.Provider]; !ok {
		return spec, invalid("agent connection is undefined")
	}
	if !effortValid(a.Reasoning) || !investigationValid(a.Investigation) {
		return spec, invalid("invalid agent effort")
	}
	if spec.Language == "" {
		spec.Language = s.ReviewLanguage
	}
	limits, err := parseLimits(a)
	spec.Budget = limits
	return spec, err
}

func effortValid(s string) bool {
	switch s {
	case "", "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func investigationValid(value string) bool {
	return value == "" || value == "light" || value == "full"
}

func parseLimits(a Agent) (domain.BudgetLimits, error) {
	var l domain.BudgetLimits
	for _, field := range []struct {
		value string
		out   *domain.Money
	}{{a.Soft, &l.Soft}, {a.Hard, &l.Hard}} {
		if field.value != "" {
			v, err := budget.Parse(field.value)
			if err != nil {
				return l, invalid("invalid exact USD budget")
			}
			*field.out = v
		}
	}
	if l.Hard > 0 && l.Soft > l.Hard {
		return l, invalid("soft budget exceeds hard budget")
	}
	return l, nil
}

func validateMCPGrants(m MCPServer) error {
	if (len(m.ReadOnlyTools) > 0 || len(m.FullTools) > 0 || m.Policy == "full") && !m.Trusted {
		return invalid("MCP tool grants require explicit trusted designation")
	}
	if m.Policy != "full" && len(m.FullTools) > 0 {
		return invalid("full MCP tools require full policy")
	}
	if m.TimeoutSeconds < 0 || m.MaxOutputBytes < 0 {
		return invalid("invalid MCP limits")
	}
	return validateToolNames(append(append([]string{}, m.ReadOnlyTools...), m.FullTools...))
}

func validateToolNames(names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if !safeName(name) || seen[name] {
			return invalid("invalid or duplicate MCP tool grant")
		}
		seen[name] = true
	}
	return nil
}

// ResolveSetup converts a configured setup role without adding it to review participants.
func ResolveSetup(s Settings) (domain.AgentSpec, error) {
	return agentSpec(s, s.Setup, domain.RoleSetup)
}
