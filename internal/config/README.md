# Configuration interface

The application supplies an absolute `Home`, a prepared source `Snapshot`, and
explicit environment/CLI overrides to `Resolve`. Resolution never reads the
current directory, environment credentials, or mutable checkout HEAD. Repository
settings and instructions come from `Snapshot.StorePath` at the exact commit in
`Snapshot.RulesRevision`.

Layers merge in this order: built-in defaults, `Home/settings.yaml`, fixed
repository `.qual/settings.yaml`, `Home/projects/<project-id>/settings.yaml`,
caller-designated `TrustedCI`, supported environment, CLI. Maps merge by key;
lists replace. Restricted reviewer list replacements preserve externally authorized
hard limits for retained IDs and reject previously unauthorized IDs when a capped
reviewer set exists. Removed reviewers are not resurrected. The environment map accepts `QUAL_UI_LANGUAGE` and
`QUAL_REVIEW_LANGUAGE`; the application selects `QUAL_HOME` before calling.
Unknown fields, duplicate keys and multiple YAML documents fail closed.

A user settings example:

```yaml
schema_version: 1
connections:
  primary:
    kind: openai
    credential: env:OPENAI_API_KEY
reviewers:
  - id: correctness
    provider: primary
    model: selected-model
    investigation: full
    soft: "1.000000000"
    hard: "2.000000000"
leader:
  id: leader
  provider: primary
  model: selected-model
setup:
  id: setup
  provider: primary
  model: selected-model
ui_language: en
review_language: en
mcp_servers:
  documentation:
    transport: stdio
    command: [documentation-server]
    policy: read-only
    trusted: true
    read_only_tools: [search, read]
    timeout_seconds: 30
    max_output_bytes: 65536
mcp: [documentation]
```

Connection kinds are `openai`, `chatgpt`, `codex`, `anthropic`, `gemini`,
`openrouter`, `openai-compatible` and the compatibility spelling `compatible`.
Connections also accept `endpoint` and `capabilities` (`tools`, `streaming`,
`reasoning`). Credentials must be `env:NAME` or `profile:name` references.
Endpoint URLs cannot contain userinfo, query strings or fragments. Billing and
route selection remain application responsibilities; config performs no fallback.

Investigation accepts `light` or `full` (or an omitted value for the app default),
independently of the provider reasoning levels. Agent options also include `reasoning`, `focus`, `instructions`, `language` and
exact decimal `soft`/`hard` USD limits. Final review roles use `domain.AgentSpec`;
`ResolveSetup` produces the separate setup spec. Empty configurations are valid
for configuration/init commands; configured reviewers require exactly one leader.
Provider capabilities and selected model availability must be checked by the app.

MCP connections accept `transport: http` with `url`, or `transport: stdio` with
`command`. Credentials are references. Explicit `trusted: true` is required for
tool allowlists; `full_tools` additionally requires `policy: full`. Neither a
server's read-only hint nor `policy: read-only` alone grants any tools. The app
must enforce these lists and default timeout/output bounds in its MCP adapter.

Repository settings and setup proposals cannot contain `trust`, `connections`,
`mcp_servers` or any agent `hard` limit, even when a value matches an existing
setting. Repository settings can select already-authorized named connections and
MCP servers. `TrustedCI` and CLI bytes are trusted because the caller designates
them; no YAML field self-authorizes a layer. Persist project trust only through
`ApplyRequest.Trust` in personal project settings. Trust changes guidance, never
tool permissions.

Discovery supports `AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, root
`.github/copilot-instructions.md`, `.qual/rules/`, `Home/rules/` and personal
project `rules/`. Additional files use `instruction_files: [{path: docs/rules.md,
glob: '*.go'}]`; globs follow Go `path.Match` relative to the rule's scope and do
not cross directory separators. Nested instructions retain their directory scope.
Rule records preserve source, path, revision, SHA-256, scope and content. Scopes
must remain attached when creating role prompts. Files are bounded to 256 KiB,
16 directory levels and 2 MiB total. Depth-pruned personal subtrees produce
warnings; discovery retains at most 128 warnings. Unsupported imports are never
followed;
missing explicit files, symlinks and oversized instructions produce warnings.

`Effective.Redacted` returns JSON containing credential references, effective
settings, domain agents, provenance, scoped rules, warnings and role guidance.
Save it as a session artifact before executing any role. It does not resolve
credentials; never add resolved secrets before saving it.

`Proposal.Settings` contains YAML bytes and `Proposal.Rules` maps safe relative
rule names to contents. `Preview` returns readable YAML for user approval.
`ApplyRequest.Context` must contain the prepared snapshot used by this run,
including its fixed `RulesRevision` and matching `ProjectID`. Its `Home` may be
omitted to use `ApplyRequest.Home`; a different home or project identity fails.
For example, `ApplyRequest{Home: req.Home, ProjectID: string(req.Snapshot.ProjectID),
Context: req, Proposal: proposal, Accepted: true}` carries the existing `Request`.
`Apply` performs no changes unless `Accepted` is true. It persists personal
project settings/rules, preserving other project metadata and existing rules.
It rejects symlinks, validates user + immutable repository + staged personal
settings, and then validates the complete supplied run context. Persistent
validation excludes `Context.TrustedCI`, `Context.Environment` and `Context.CLI`;
transient run overrides cannot make otherwise invalid saved settings acceptable.
Proposal reviewer replacements retain authorization independently of the currently
selected roles. Protected `.config-reviewer-authorization.yaml` metadata in the
same project generation records authorized IDs, hard limits, user/personal origins
and whether a field was selected. Removing every reviewer does not erase these
constraints or reintroduce removed reviewers. Repository settings and proposals
cannot write this metadata.

Generated copies retain user origin: unchanged inherited hard fields follow the
latest global user limit during resolution and application. A changed or newly
added trusted personal hard field takes personal precedence; zero or an omitted
hard field explicitly clears that personal cap. Historical personal constraints
remain when a proposal removes a selection, and current explicit personal fields
supersede their historical values. Final trusted CI/CLI layers retain normal run
precedence and are not persisted as personal authority. It stages all files with owner-only permissions before installing the directory.
A per-project nonblocking OS file lock excludes cooperating writers. Every
writer of this personal project tree must honor `<project>.config.lock`; manual
concurrent edits are outside this locking protocol. A failed staged write leaves
the active directory intact. Installation retains a sibling
`<project>.config.backup` until successful replacement and rolls back rename
failures; the next resolution, discovery or accepted apply recovers an interrupted
rename. Directory
replacement uses two renames. Both public `Resolve` and `Discover` take the same
writer lock across settings and scoped rules reads and recover interrupted backup
installation before reading. A busy lock fails closed instead of returning a
missing or mixed generation. Internal staged validation reuses the already-held
lock. Lock files persist and OS locks release on exit.
