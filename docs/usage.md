# Using Qual

Build with `just build`; the executable is `bin/qual`. Service commands use readable
human summaries/tables by default; add `--json` for machine output. Doctor shows
installed tools and sandbox launcher availability separately from active isolation.
A trusted `scripts/qual-sandbox` beside `bin/qual-preview` is discovered even when
it is outside PATH; doctor does not execute the launcher. Qual uses Git and stores
sessions, immutable evidence, snapshots, worktrees and personal settings under
`QUAL_HOME` (default `~/.qual`). Keep this directory outside the reviewed checkout.

## Connect a provider

Run `qual init` in a terminal. It is the single place to connect providers,
choose accounts and models, set budgets and language, and preview settings before
saving. Use ↑/↓ to choose, Enter to continue, and Esc to cancel. API keys stay
hidden. Browser sign-in displays its authorization URL in the same interface.
No inference request is made during initialization.

```sh
qual init
qual review
```

The reviewer investigates changes; the leader verifies the findings independently.
The wizard can use the same model for both roles, or a different leader model.
Model descriptions show known capabilities and input/output prices per million
tokens. Spending thresholds stop new requests using recorded usage and estimated
reservations; the final provider bill can exceed an estimate. Catalog entries are not promises of account access: init checks selected
models against the account before saving. Unknown model capabilities remain
explicitly unknown. Custom deployments can declare verified capabilities during init with
`--capability tools --capability streaming` (and `--capability reasoning` when
supported). These are explicit overrides from your provider's documentation;
Qual does not infer capabilities from an unfamiliar model name.

Run init again to add an account. Choose **Sign in again** to repair an existing
account while retaining its credential reference for saved sessions. Existing connections and project settings are
preserved; choose whether to keep current review defaults. A project that requests
an unconfigured provider/model reports the required names and configuration source,
with an exact init command. Initialization does not start or resume a review.

For automation, provide all choices and explicit acceptance:

```sh
qual init --connection primary --kind openai --credential env:OPENAI_API_KEY \
  --model gpt-6-luna --leader-model gpt-6-luna --reasoning-effort low \
  --non-interactive --accept
# Add an account required by a project, retaining current role defaults:
qual init --connection team --kind openai --credential env:TEAM_API_KEY \
  --model gpt-6-luna --add-account --non-interactive --accept
```

Without acceptance, init only previews. Failed authentication, unavailable models,
and cancellation leave active settings intact. `--soft` and `--hard` are exact USD
limits for each role; the terminal distinguishes unknown cost from zero. A USD
ceiling requires known provider pricing. See [the configuration interface](../internal/config/README.md)
for advanced role settings.

`auth login --kind KIND --profile NAME --stdin` or `--key-env NAME` remains an
advanced credential-import command. Interactive provider and model selection
belongs to `qual init`; bare `auth login` points there.

On Linux, the maintained libsecret `secret-tool` client supplies OS keyring access
when installed in a D-Bus session. Otherwise Qual selects owner-only credential
files. `QUAL_AUTH_BACKEND=file|keyring` explicitly chooses the backend before its
first use; the choice is persisted in `QUAL_HOME/auth-backend`. A failure in a
selected keyring fails authentication and never relocates credentials. API-key
profile records are provider-specific. `auth list` exposes metadata only;
`auth logout PROFILE` removes the selected record.

Choose **ChatGPT account** in init for token-sharing OAuth. This requires your
own enabled OAuth application and its configured client ID; init reports any
missing prerequisite. **Legacy Codex account** is a separate compatibility route.
`qual init --kind codex --device` supports device authorization for that legacy route. Qual never switches billing routes on failure.

`models list --connection primary` shows capabilities and known prices with their
catalog version. API catalogs are capability/pricing references, not account
entitlement promises; ChatGPT discovery queries the selected account.
`doctor --connections` performs unpaid authentication probes, not inference.

## Investigate project conventions

```sh
qual setup /path/to/repo --trust untrusted
qual setup /path/to/repo --trust trusted --non-interactive --accept --json
```

Setup chooses trust before model exploration, then uses the same tools, budgets,
validated finish and session storage as review. Trust affects instructions, not
OS permissions. Review and setup report whether an external launcher reports an
active sandbox and its network mode. The marker is informational and grants no
capabilities. Direct invocation reports the external sandbox as “not reported”.

Setup previews proposed YAML and rules with their personal destination under
`QUAL_HOME/projects/<project-id>/` before applying them. Rules become numbered
`setup-NNN.md` files. The original proposal also preserves commands, reasons and
questions as immutable evidence. Rejection leaves personal settings untouched;
noninteractive setup applies changes only with `--accept`. Proposals cannot grant
credentials, endpoints, full MCP access, trust or higher hard limits. The separately
chosen trust value is saved only when the proposal is accepted.

## Review a snapshot

```sh
qual review                         # tracked working changes against HEAD
qual review ./service --staged       # index against HEAD
qual review --base origin/main      # origin/main against HEAD
qual review --base BASE --head HEAD  # explicit commit pair
qual review --worktree --include-untracked
qual review https://github.com/org/repo/pull/123
qual review https://gitlab.com/group/repo/-/merge_requests/123
qual review --staged --non-interactive --json --output ./reports
```

`--staged`, `--worktree` and `--head` are mutually exclusive. `--head` requires
`--base`; untracked files require working-tree mode. URL reviews determine their
own sides and reject local selectors. `GITHUB_TOKEN` and `GITLAB_TOKEN` provide
explicit host credentials for their public hosts. `GITLAB_TOKEN` is never reused
for a self-managed host. `QUAL_CI_CHECKOUT` can identify the trusted CI checkout.

For a self-managed GitLab instance, choose **Self-managed GitLab (repository
access)** in `qual init`, or configure that host directly:

```sh
qual init --gitlab-host gitlab.example.com
# Automation persists the environment reference, not its token value:
qual init --gitlab-host gitlab.example.com --gitlab-token-env COMPANY_GITLAB_TOKEN \
  --non-interactive --accept
qual review https://gitlab.example.com/group/repo/-/merge_requests/456
```

The terminal hides pasted host tokens. Use `--gitlab-public` to trust a public
instance without authentication. Host-only init preserves model defaults and
makes no model request. Trusted hosts and credential references live only in
owner-only `QUAL_HOME/platform-hosts.json`; project settings cannot add hosts or
redirect their authentication. Prompted tokens use protected credential storage,
bound to the exact host (including an optional HTTPS port). Review and resumed
sessions resolve the currently configured credentials for that host. An unknown
host or unavailable host token names the init command needed to configure it.

In a terminal, review, setup and resume show a live timeline with model roles,
file/tool activity, elapsed time, spending and reservations. Ctrl+O expands safe
activity details; ↑/↓ scroll; Esc or Ctrl+C interrupts work and waits for the
session to be saved. Questions appear in the same terminal owner. Resizing reflows
the view. Completion returns to the shell with confirmed findings, a report path,
and session commands. A cancelled session also shows its resume command.

Redirected streams, `--json`, `--non-interactive` and `TERM=dumb` use plain output;
`NO_COLOR` keeps interactive controls while disabling colors. JSON progress stays
JSONL, including browser authorization instructions. No terminal escape sequences
are added in plain or JSON output.

Qual prepares source exactly once. Repository settings/rules come from its fixed
rules revision, never a second moving checkout capture. Each primary reviewer
and the mandatory leader receives the same immutable initial tree in independent
worktrees. Clean diffs save a `no_changes` session with no model calls and require
neither configured agents nor provider credentials.

`--focus` accepts general, correctness, architecture, maintainability, security or
tests. `--instructions` appends reviewer guidance. `--reasoning-effort` and
`--investigation-effort` are separate controls (`light`, the default, or `full` for investigation); `--ui-language` and
`--review-language` are separate preferences. CLI reviewer overrides preserve
independent leader settings. For distinct per-reviewer models, budgets, languages
and focuses, use the reviewer list and leader object in YAML.

UI interaction and diagnostic labels support `en` and `ru`; unsupported UI locales
fail explicitly. Agents receive the UI locale for questions separately from their
report language. Review output may use another language, for example
`--ui-language ru --review-language fr`. Machine field names and error codes remain
stable; technical error details retain adapter context.

Progress uses stderr; `--json` makes progress, proposal previews, acceptance,
questions and command errors JSONL and final stdout one versioned
run-state JSON object. Human mode prints the Markdown report. `--output DIR`
writes `report.json` and `report.md`; reports and configuration provenance are also
session artifacts. Reports contain snapshot identity, role results, finding
anchors, leader decisions, usage, costs, configured limits, questions and
assumptions. Unknown usage/prices remain unknown. Incomplete, stale, cancelled
and failed runs exit nonzero. Findings alone succeed; `--fail-on-critical` returns
nonzero for leader-confirmed critical findings.

Direct execution uses ordinary process permissions. To apply the independently
reviewed Linux boundary before starting Qual, use
[the external sandbox launcher](sandbox.md). Worktrees alone are not an OS sandbox.

## Sessions and tools

```sh
qual sessions list --json
qual sessions show RUN_ID --json
qual sessions cancel RUN_ID
qual sessions resume RUN_ID --non-interactive --json --output ./reports
qual sessions export RUN_ID --output ./session.json
qual config show --path /path/to/repo
qual config validate
qual mcp list
qual mcp test SERVER
qual cleanup                           # all terminal, unpinned worktrees
qual cleanup --ttl 336h                  # optional explicit 14-day age filter
qual doctor
qual ci generate github --output ./generated-ci
```

SIGINT/SIGTERM cancel the active runtime and its processes while preserving the
checkpoint. `sessions cancel` also signals an owner in a different process.
`sessions show` adds an allowlisted execution history: role/model/phase, tool
actions, success/failure and result sizes, spent/reserved/soft/hard budget values
(in nanodollars), and immutable artifact references. It omits tool arguments, raw
outputs, private checkpoints and provider continuation data. Live progress uses
the same safe view and includes budget threshold/finalization notices.

Resume keeps the original run ID, snapshot, agent specs, configuration references
and ledger, resolving current credential values from those references. It ignores
new user/project settings for that run. Normal command invocation automatically collects terminal, unpinned worktrees
older than 14 days. Manual `qual cleanup` removes all terminal, unpinned disposable
worktrees regardless of age; `--ttl` adds an optional minimum age. The automatic
pass is skipped before manual cleanup. Missing home/registry paths are not created
for collection. Automatic failures emit a warning while the requested command
continues; manual failures remain command failures. Active/pinned worktrees,
session evidence, settings and snapshots remain available.

Exports are versioned JSON diagnostic archives, including redacted events,
immutable artifacts and integrity hashes. They exclude private provider
continuation data; they are not a migration format for private resumable state.
Without `--output`, export writes the archive directly to stdout.

MCP list/test perform actual handshake and schema discovery without invoking tools.
Only selected servers are connected. Explicit trusted tool allowlists determine
capabilities; server read-only hints never grant permission. Full outputs and audit
records use the current session store and are accessible through `read_artifact`.

See [CI integration](ci.md) for trusted event authorization and the exact
`--expected-head` / `--expected-target` handoff. Both compare the single prepared
snapshot before model execution. Memory, web search and publishing are phase 2
and are absent from the command tree.

## Offline verification

`just lint` runs strict formatting and lint checks. `tests/e2e/run.sh` executes
built CLI processes against real Git/files and local Responses/MCP fixtures with
the race detector. `tests/e2e/sandbox.sh` additionally runs the real CLI through
the Linux launcher against a localhost Responses fixture with network mode `open`.
The fixture mounts an explicitly selected Go toolchain and executes real Go tests
through the agent shell with private HOME/cache/temp directories and no inherited
model credential. It requires provisioned namespace permissions; failures are not skipped. The
sandbox boundary suite separately tests restricted and disabled networking.
