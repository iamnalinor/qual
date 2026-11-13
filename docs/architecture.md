# Architecture

One module github.com/iamnalinor/qual, one qual binary. Go 1.27.4. Linux runtime and Linux external sandbox; macOS direct runtime can be compiled but sandbox is phase 2.

## Boundaries

- internal/domain: identifiers, snapshots, findings, usage, money, events and configuration value types. Pure stdlib data/validation; no os, exec, HTTP or adapters.
- internal/runtime: common setup/reviewer/leader loop, tools and orchestration through narrow ports. May import domain and explicitly shared pure packages, never concrete adapters.
- internal/{gitstore,platform,provider,session,config,auth,mcp,report}: infrastructure and focused services. Each consumes domain; runtime interfaces implemented structurally. No cycles.
- internal/diagnostic: safe printable adapter causes, with explicit credential redaction and preserved error classification.
- internal/app and internal/cli: composition and commands; cmd/qual is minimal main.
- internal/tui: one Bubble Tea event loop owns terminal input, rendering, prompts, scrolling and cancellation. Application events feed the same UI during init, setup, review and resume; JSON/noninteractive invocations bypass it.

Use Cobra and strict YAML decoding, stdlib net/http, os/exec, encoding/json, slog and testing. MCP uses official Go SDK. No database or server is needed: QUAL_HOME owns event journals, immutable artifacts, atomic checkpoints and locks.

## Public contracts

CLI: init, setup [path], review [target], sessions list/show/resume/cancel/export, config show/validate, auth login/list/logout, models list, mcp list/test, cleanup, doctor, ci generate github/gitlab. Support help, JSON and non-interactive behavior. Unsupported phase-2 capabilities fail explicitly.

Review target defaults to '.', accepts local directory or PR/MR URL only. Local selectors are mutually exclusive staged/worktree/head; head requires base. Empty diff makes no model call. Findings alone exit successfully unless fail-on-critical. Stable errors distinguish invalid config, auth, unsupported capability, unavailable source, budget, tool, stale and incomplete.

Snapshot stores project/run identity, source kind, base/head commit and tree IDs, target SHA, diff hash, changed file manifest and rules revision. PR diff is merge-base(target, head) -> head; explicit local refs compare directly. Dirty snapshots create technical commits only in Qual's independent store. All agents get identical initial right tree. Source index, refs and working files never change.

Provider uses Start/Resume and streamed normalized events for text, tool-call, usage, completion and errors; tools execute only in Qual runtime. No App Server. OpenAI API and ChatGPT token-sharing use Responses protocol with different credentials/capabilities; legacy Codex route explicitly selected. Anthropic/Gemini/OpenRouter/compatible implement same semantics. Opaque provider continuation data may be stored without exposing hidden reasoning to visible logs.

Each role owns worktree, transcript, context and budget. Primary reviewers finish before mandatory leader. finish accepts file paths to role-specific outputs; validate confinement, schema, size and snapshot, then preserve hashed copies. Leader records disposition of every primary finding. Text response alone is not finish. The initial contract includes the exact agent identity and typed JSON examples bound to the current run and diff. Primary-dependent leader examples remain in user-role messages alongside saved primary results; model-produced data never enters the system example. Retained primary findings preserve both their ID and reviewer identity; new leader findings use the leader identity. Validation errors identify the offending finding and field so agents can repair invalid outputs.

Session writes effect intent before execution and result afterward; atomic checkpoint references journal position. Resume reuses run ID, snapshot and ledger. Never replay confirmed tool effects; unknown modifying outcome requires explicit resolution. One owner lock per run, cancel kills owned process groups. Compaction never deletes evidence or resets money. Its default watermark is 96 KiB of serialized request data; verified model context limits can require earlier compaction. It retains the system contract, fixed snapshot, and the leader's exact original primary-results user message alongside the summary and immutable transcript reference.

Money is fixed precision integer nanodollars, checked for overflow. Reservations are atomic, retries/compacts billed to their role. Unknown usage remains unknown. Subscription allowance is separate from actual metered USD; never fabricate a price or promise precise hard bounds where provider cannot support them.

Environment setup is centralized in qual init. Missing project model credentials report their model, account and configuration origin, with a command that adds local access while preserving existing default models.

Self-managed GitLab host trust lives in the owner-only QUAL_HOME/platform-hosts.json, outside project-controlled settings. qual init configures host-specific environment or protected credential references; prompted tokens use the auth store. Source composition resolves only the selected host and never substitutes the GitLab.com token for a custom host.

Project settings cannot grant trust, credentials, endpoint changes, MCP full, sandbox bypass or hard-limit increases. PR settings/rules come from fixed target revision, local dirty from HEAD, explicit refs from left commit. Non-interactive defaults to untrusted. Secret refs only in config; credentials in owner-only storage or OS keyring.

External qual-sandbox applies independent copy/mount/network boundary before starting qual. Linux uses bubblewrap and pinned sandbox-runtime proxy, with none/restricted/open network modes. No unsafe fallback. Worktrees isolate experiments, not hostile processes from each other.

## Verification

Ordinary tests contain no live service calls. Integration/e2e use real git, disk, subprocess, HTTP/SSE and MCP fixtures. Live build tag and just recipe are separate. Main CI runs lint, build, race and offline e2e plus sandbox boundary tests. Live tests manual only. Versioned report JSON and YAML config are user interfaces; Go internal packages are not a public SDK.
