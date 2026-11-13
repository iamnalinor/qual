// Package config resolves strict settings and scoped instructions from fixed snapshots.
package config

import "github.com/iamnalinor/qual/internal/domain"

// Connection contains references only, never resolved credentials.
type Connection struct {
	Kind         string   `yaml:"kind" json:"kind"`
	Endpoint     string   `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Credential   string   `yaml:"credential,omitempty" json:"credential,omitempty"`
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
}

// Agent configures a single independent role; money is exact decimal USD.
type Agent struct {
	ID            string `yaml:"id" json:"id"`
	Provider      string `yaml:"provider" json:"provider"`
	Model         string `yaml:"model" json:"model"`
	Reasoning     string `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`
	Investigation string `yaml:"investigation,omitempty" json:"investigation,omitempty"`
	Focus         string `yaml:"focus,omitempty" json:"focus,omitempty"`
	Instructions  string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
	Language      string `yaml:"language,omitempty" json:"language,omitempty"`
	Soft          string `yaml:"soft,omitempty" json:"soft,omitempty"`
	Hard          string `yaml:"hard,omitempty" json:"hard,omitempty"`
}

// MCPServer is an externally authorized connection and policy.
type MCPServer struct {
	Trusted        bool     `yaml:"trusted,omitempty" json:"trusted,omitempty"`
	ReadOnlyTools  []string `yaml:"read_only_tools,omitempty" json:"read_only_tools,omitempty"`
	FullTools      []string `yaml:"full_tools,omitempty" json:"full_tools,omitempty"`
	TimeoutSeconds int      `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`
	MaxOutputBytes int      `yaml:"max_output_bytes,omitempty" json:"max_output_bytes,omitempty"`
	Transport      string   `yaml:"transport" json:"transport"`
	Command        []string `yaml:"command,omitempty" json:"command,omitempty"`
	URL            string   `yaml:"url,omitempty" json:"url,omitempty"`
	Credential     string   `yaml:"credential,omitempty" json:"credential,omitempty"`
	Policy         string   `yaml:"policy" json:"policy"`
}

// InstructionFile explicitly selects an additional repository file and applicability glob.
type InstructionFile struct {
	Path string `yaml:"path" json:"path"`
	Glob string `yaml:"glob,omitempty" json:"glob,omitempty"`
}

// Settings is the versioned YAML interface.
type Settings struct {
	SchemaVersion    int                   `yaml:"schema_version,omitempty" json:"schema_version"`
	Connections      map[string]Connection `yaml:"connections,omitempty" json:"connections,omitempty"`
	Reviewers        []Agent               `yaml:"reviewers,omitempty" json:"reviewers,omitempty"`
	Leader           Agent                 `yaml:"leader,omitempty" json:"leader"`
	Setup            Agent                 `yaml:"setup,omitempty" json:"setup"`
	Trust            string                `yaml:"trust,omitempty" json:"trust"`
	UILanguage       string                `yaml:"ui_language,omitempty" json:"ui_language,omitempty"`
	ReviewLanguage   string                `yaml:"review_language,omitempty" json:"review_language,omitempty"`
	MCPServers       map[string]MCPServer  `yaml:"mcp_servers,omitempty" json:"mcp_servers,omitempty"`
	MCP              []string              `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	InstructionFiles []InstructionFile     `yaml:"instruction_files,omitempty" json:"instruction_files,omitempty"`
}

// Origin identifies the winning layer without retaining its values.
type Origin struct {
	Source   string `json:"source"`
	Path     string `json:"path,omitempty"`
	Revision string `json:"revision,omitempty"`
}

// Request uses a prepared snapshot and explicit overrides, never the current directory.
type Request struct {
	Home        string
	Snapshot    domain.Snapshot
	Environment map[string]string
	CLI         []byte
	TrustedCI   []byte
}

// Effective is safe to persist as a session artifact: credentials remain references.
type Effective struct {
	Settings   Settings           `json:"settings"`
	Agents     []domain.AgentSpec `json:"agents"`
	Provenance map[string]Origin  `json:"provenance"`
	Rules      []Rule             `json:"rules"`
	Warnings   []string           `json:"warnings,omitempty"`
	Guidance   string             `json:"guidance"`
}
