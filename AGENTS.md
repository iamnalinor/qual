# Working on Qual

Qual is a Go CLI for AI code review. The main branch is `main`.

## Get running

Use Go 1.27.4. On Linux x86_64, install the pinned tools and dependencies:

```sh
./scripts/bootstrap
export PATH="$PWD/.tools/bin:$PATH"
just build
./bin/qual --help
```

Bootstrap needs network access. Builds and ordinary tests need no model credentials.
For the full environment requirements, see [CONTRIBUTING.md](CONTRIBUTING.md).

## Where to make a change

| Change | Start here |
| --- | --- |
| Command, flag, help text or exit behavior | `internal/cli/`; entry point: `cmd/qual/` |
| User flow or connecting components | `internal/app/` |
| Interactive screens, keyboard handling or terminal layout | `internal/tui/` |
| Reviewer/leader execution, tool calls or cancellation | `internal/runtime/` |
| Shared data types or adapter interfaces | `internal/domain/`, `internal/port/` |
| Provider requests, streaming or model catalog | `internal/provider/` |
| Login, token refresh or credential storage | `internal/auth/` |
| Safe, actionable external error causes | `internal/diagnostic/` |
| Settings, precedence or project rules | `internal/config/` (schema examples in its `README.md`) |
| Git snapshots, worktrees or cleanup | `internal/gitstore/` |
| GitHub PR or GitLab MR loading | `internal/platform/` |
| Session persistence, journal or resume data | `internal/session/` |
| Cost accounting and limits | `internal/budget/` |
| Final reports and finding anchors | `internal/report/` |
| MCP connections and tools | `internal/mcp/` |
| Generated CI workflows | `internal/citemplate/templates/` |
| Linux sandbox boundary | `scripts/qual-sandbox`, `sandbox/`, `tests/sandbox/` |
| This repository's CI | `.github/workflows/` |

For a new command, add CLI parsing in `internal/cli`, put the operation in
`internal/app`, and add a subprocess scenario beside the existing CLI tests.
For a new external integration, implement a port in its adapter package and wire
it in `internal/app`; keep infrastructure out of `internal/domain`.
[docs/architecture.md](docs/architecture.md) explains the execution and storage model.

## Check the change

```sh
just fmt
just lint
go test -race ./internal/PACKAGE/...
just check
```

Replace `PACKAGE` with the package you changed. Tests live next to their code;
CLI integration tests use real Git repositories and local HTTP fixtures.
`just check` runs the complete offline lint/build/test set.

- CLI behavior: `just test-e2e`.
- Linux isolation: `just test-sandbox`; provisioning is in `CONTRIBUTING.md`.
- Real models: `just test-llm`, separately enabled; see `tests/live/README.md`.

Formatting and complexity limits are defined in `.golangci.yml`; code and comments
are English. For user-visible changes, check both terminal output and `--json`.

## Update the matching docs

User-facing commands belong in `docs/usage.md`; keep the first-run path in
`README.md` short. Development instructions belong in `CONTRIBUTING.md`.
Update `docs/architecture.md` when package responsibilities or persisted session
behavior changes. Build and test recipes belong in `justfile`.
