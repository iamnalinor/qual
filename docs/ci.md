# CI integration

`qual ci generate github|gitlab` emits editable workflow YAML and installation notes.
The embedded canonical sources live in `internal/citemplate/templates/`. Read the
provider setup notes before enabling a generated workflow: they require a pinned
trusted Qual source, explicit read-only source authentication and trusted settings.
No model credentials or trigger secrets are generated. No publication is enabled.

Repository verification separates `just check` (lint/build/offline Go race tests)
from `just test-sandbox` (required provisioned Linux boundary tests). Sandbox tests
need Python3, Node >=22.12, bubblewrap, socat, ripgrep, which, and
`npm ci --ignore-scripts --prefix sandbox`. Required namespace operations must be
allowed by the runner; failure is not a skipped test. `just test-e2e` runs the CLI
against real Git and local HTTP fixtures. `just test-llm` runs the separate real-model
suite with explicit opt-in and a persistent budget gate; see [live test setup](../tests/live/README.md).
GitHub verification does not make paid model calls. Run live checks locally with
the same persistent budget ledger; they are not represented by a placeholder CI job.

Scripts are linted by extension and shebang: shell uses ShellCheck, Python uses
`py_compile` with an external cache, and Node uses `node --check`. Sandbox dependency
sources are pruned, not our launcher/profile. Actionlint validates repository and
generated GitHub workflows. No check silently ignores a missing interpreter/tool.

## Download CI binaries

Every push to main and pull-request run builds four independent artifacts:
`qual-linux-amd64`, `qual-linux-arm64`, `qual-darwin-amd64`, and
`qual-darwin-arm64`. Open the **Offline checks** run in GitHub Actions and download
its matching artifact, or use GitHub CLI:

```sh
gh run list --repo iamnalinor/qual --workflow check.yml --branch main
gh run download RUN_ID --repo iamnalinor/qual \
  --name qual-linux-amd64 --dir ./qual-download
cd qual-download
tar -xzf qual-linux-amd64.tar.gz
sha256sum --check SHA256SUMS
./qual --help
```

Choose `darwin-arm64` for Apple Silicon or `darwin-amd64` for Intel macOS. On macOS,
verify with `shasum -a 256 -c SHA256SUMS`. The tar archive preserves the executable
mode and includes `qual`, `SHA256SUMS`, and `BUILD-INFO.txt` recording source commit,
Go version and target. Builds use Go 1.27.4 with CGO disabled and standard Go VCS
metadata, without application-specific version linker flags. Binary artifacts remain
available for 14 days; diagnostics remain separate. Select a trusted main run and
check its commit before executing a download. These are CI artifacts, not published
releases. The core CLI is available on macOS; the external sandbox remains Linux-only.

## Application contract

The composition edge wires `citemplate.Generate(provider) (Files,error)` and
`citemplate.Authorize(ctx, Input, HTTPDoer) (Decision,error)` into CLI commands.
`Files` has `Workflow` and `Setup`. `Input` takes `Provider`, `Event`, raw `Payload`,
trusted `Repository` and `APIURL`, read-only platform `Token`, `TrustedSource`, and
manual `ManualNumber`/`Actor`. The HTTP client must reject credential-bearing
redirects to different origins. Authorization has a 30-second total deadline.
`TrustedSource` is the trusted caller's assertion, never read from an event field.
The workflow must enforce that context before setting it; this is not a signature
verifier for arbitrary unauthenticated webhooks.

The generated command contract is:

```sh
qual ci authorize --provider github --event-file "$GITHUB_EVENT_PATH" \
  --event "$GITHUB_EVENT_NAME" --repository "$GITHUB_REPOSITORY" \
  --api-url "$GITHUB_API_URL" --actor "$GITHUB_ACTOR" \
  --number "${QUAL_NUMBER:-0}" --trusted-source
```

For GitLab the corresponding trusted context is `CI_PIPELINE_SOURCE`, `CI_PROJECT_ID`,
`CI_API_V4_URL`, `GITLAB_USER_ID`, and `QUAL_MR_NUMBER`; trigger payload bytes come
from the `TRIGGER_PAYLOAD` file. Credentials are read from caller-provided
`GITHUB_TOKEN`/`GITLAB_TOKEN`, never CLI arguments or payloads. Output is one JSON
object with `provider`, `repository`, `number`, `url`, `actor`, `target_sha`, `head_sha`.
Errors use stable prefixes: `ci_invalid`, `ci_untrusted`, `ci_event`, `ci_skipped`,
`ci_denied`, `ci_api`, `ci_snapshot`, `ci_unsupported`, and `ci_template`. Skipped
and denied decisions do not produce an approved JSON object or start a review.

The caller passes captured SHAs into the sandboxed command:

```sh
qual review "$REVIEW_URL" --expected-head "$REVIEW_HEAD" \
  --expected-target "$REVIEW_TARGET" --non-interactive --output /output
```

These guards compare the prepared snapshot's source H and target T before the first
model call. T is the target tip, not merge-base B. Source movement never silently
changes the approved snapshot. The sandbox selects only trusted settings and explicit
owner-only credential files, and retains reports/session evidence on all outcomes.
The application end-to-end fixture must exercise this exact authorization-to-review
handoff, including rejected permission, drafts and moving SHAs, before claiming full
CI review execution is verified. Syntax/event tests alone do not establish that claim.

`just vuln` queries the Go vulnerability database separately from offline checks.
`just vuln-sandbox` queries npm advisories and currently reports the documented
[upstream Zod advisory assessment](sandbox-dependencies.md); it must not be treated
as a clean audit or hidden through an incompatible dependency override.
