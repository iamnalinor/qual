# Guarded live checks

Ordinary tests stay offline. `tests/live/gate` tests use localhost fake HTTP/SSE,
real disk and separate processes. The `llm` build tag contains the actual Qual
reviewer/leader smoke, including SIGINT and resume of the same snapshot and role
ledger. It requires explicit opt-in and retains all artifacts.

The gate enforces a persistent USD10 development budget across all live runs. The existing
`artifacts/live-budget.json` contains the cumulative availability/tariff receipt
and attempt history. Preserve this ignored local file across checkouts and runs;
never reset its balances, discard reservations or copy it as a fresh budget.
A missing or malformed ledger fails closed. The gate never initializes one.

For a new checkout with no prior development spending, a developer must first
verify model availability for the intended account and the tariff against the
[official model documentation](https://developers.openai.com/api/docs/models/gpt-6-luna).
The following sanitized example describes the receipt verified on **2026-11-13**;
it contains no paid attempt records. It is not evidence of availability for your
account. Verify it before any live use. The gate pins this exact date, source and
rates; if they no longer apply, stop and review the gate's tariff validation and
reservation calculations before using it. Bounded requests remain below the
272,000-token pricing boundary for this tier.

Only after that verification, and only for a genuinely new development budget,
create the initial ledger below. If any previous ledger exists, reuse its path
instead, including its complete history. The shell's no-clobber option prevents
this example from overwriting an existing file:

```sh
mkdir -p artifacts
(
  umask 077
  set -C
  cat > artifacts/live-budget.json <<'JSON'
{
  "schema_version": 1,
  "currency": "USD",
  "ceiling_nanodollars": 10000000000,
  "spent_nanodollars": 0,
  "reserved_nanodollars": 0,
  "selected_model": "gpt-6-luna",
  "pricing_checked_at": "2026-11-13",
  "pricing_source": "https://developers.openai.com/api/docs/models/gpt-6-luna",
  "input_per_million_nanodollars": 100000000,
  "cached_input_per_million_nanodollars": 10000000,
  "cache_write_per_million_nanodollars": 125000000,
  "output_per_million_nanodollars": 500000000,
  "records": [
    {
      "operation": "models.list",
      "model_available": true,
      "inference_requests": 0,
      "cost_nanodollars": 0
    }
  ]
}
JSON
)
```

Supply the real key as `QUAL_LIVE_UPSTREAM_KEY` in the gate process environment. Do not put a key in arguments,
settings, a transcript, reports, source files or shell history; do not shell-source
`.env`. Supply a separate cryptographically random token (at least 32 characters)
as `QUAL_LIVE_GATE_TOKEN`, and set `QUAL_RUN_LLM=1` for the gate. Start:

```sh
go run ./tests/live/gate -ledger /absolute/path/to/artifacts/live-budget.json
```

It prints only `ready http://127.0.0.1:PORT/v1`. Its upstream is fixed to
`https://api.openai.com/v1/responses`; no endpoint/key command arguments exist.
Only authorized POST Responses requests for `gpt-6-luna` are accepted. Requests
must stream, disable storage, use inline text/function traffic and reserve at
most 8192 output tokens. Hosted tools, stored-response/file/media references,
service tiers, alternate endpoints and bodies over 128 KiB are rejected.
Redirects are never followed and dispatched upstream failures return a
non-retryable HTTP status. Requests have a three-minute deadline.

Run the smoke in a separate environment containing only the gate URL/token,
opt-in and these two explicit paths:

```sh
export QUAL_LIVE_GATE_URL=http://127.0.0.1:PORT/v1
export QUAL_LIVE_LEDGER=/absolute/path/to/artifacts/live-budget.json
export QUAL_LIVE_ARTIFACT_DIR=/absolute/path/to/new-retained-run-directory
./scripts/test-llm
```

The CLI subprocess receives an isolated HOME, QUAL_HOME and the limited token as
`OPENAI_API_KEY`; it does not inherit the real upstream key or ambient credentials.
No secret is written into settings or fixture files. Source, built CLI, reports,
stdout/stderr, session events/checkpoints/artifacts and a final ledger snapshot
remain in the artifact directory, including on failure. Inspect them before
considering a rerun; the harness does not automatically rerun a failed smoke.

The deterministic fixture can also be created with
`./tests/live/create-fixture.sh /absolute/new-directory`. Its committed
`TestDivideZero` passes at HEAD and detects the dirty change that removes the
zero-divisor guard. Offline verification checks both cases. The live smoke
requires actual source/output tools and validated reviewer/leader completion;
whether it finds the known seeded bug is a separate quality assessment, never
assumed from pipeline completion alone.

Each request reserves its conservative maximum under a separate filesystem flock
before dispatch. Atomic replacement plus file/directory sync persists the ledger.
Terminal usage charges uncached/cached input and output at the receipt rates once;
unused reservation is released only after known validated usage. EOF, cancellation,
network errors, missing usage and process crashes retain the reservation. Ledger
history must agree with balances; concurrent gates share the same lock and ceiling.
Records include attempt IDs, reservation/settlement status, model, response ID,
token counts and nanodollars, never prompts, tokens, keys or hidden reasoning.

The live smoke runs direct CLI. External isolation is separately exercised by
sandbox tests. For a later sandbox pilot, you can explicitly select
launcher `--network open` to reach this localhost gate while preserving filesystem
and PID isolation; only the limited gate credential enters that sandbox. Keep the
real upstream credential in the separate gate process. Other sandbox profiles
remain independently tested.
