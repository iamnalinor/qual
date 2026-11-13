# Install the GitHub review workflow

Save the YAML as `.github/workflows/qual-review.yml` on your trusted default branch.
Protect that branch and any other PR target branches permitted to run this workflow.
The workflow uses `pull_request_target` and default-branch issue comments; it never
checks out or executes a PR source branch on the host. Manual dispatch is restricted
to the default branch. Keep this workflow and its action pins under trusted review.

Set repository variable `QUAL_SOURCE_SHA` to the full 40-character commit ID of the
Qual source you audited, including working `ci authorize` and review snapshot guards.
The default source repository, `iamnalinor/qual`, is public. This template uses
explicit authenticated source checkout: set `QUAL_SOURCE_READ_TOKEN` to a read-only
GitHub token (or GitHub App token) authorized to read that source. A private fork
requires a token with access to that fork; the destination repository's default
`github.token` normally cannot read a different private repository.
There is no assumed published release or silently selected alternate download source.
Go 1.27.4 builds the selected source; sandbox npm dependencies use the committed lock.

Before enabling review, create `.qual/ci-settings.yaml` on the trusted target branch
using your version's `qual init`/configuration schema. Select an API provider/model,
explicit budget and hard limit, credentials by environment reference, and untrusted
project behavior. The template selects `OPENAI_API_KEY` and `api.openai.com:443` as
one concrete route. Set `QUAL_OPENAI_API_KEY` as a repository secret. For another
provider, edit its settings, secret reference and exact egress host together; no
provider, billing route or authorization fallback occurs. Never commit credentials.
The GitHub token stays read-only (`contents: read`, `pull-requests: read`); use a
separate read-only platform token if the selected Qual auth adapter requires one.

Authorization resolves current PR metadata and the event initiator's repository
permission through the API before the review step receives model credentials.
Only write, maintain and admin permissions qualify. Opened, reopened, ready and
synchronize events qualify; labels require exactly `qual-review`, and newly created
PR issue comments require exactly `/qual review`. Draft/closed requests and unrelated
events produce an explicit `ci_skipped` result; permission failures are `ci_denied`.
Both stop before model calls. The exact target T and source H SHAs are captured;
review must reject either moving before it prepares its snapshot. Diff base is still
merge-base(T,H), never a substitute for T. Comments and PR text remain data.

Ubuntu 24.04 provisioning installs bubblewrap, Python3, Node 22.12.0, socat, ripgrep,
and which; the explicit AppArmor sysctl permits required nested user namespaces.
Self-hosted runners must supply equivalent isolation. A denied boundary fails the
job; it is never skipped or replaced by direct execution. No publication is enabled.

Reports (Markdown/JSON) and session traces are retained as CI artifacts for
seven days, including failures. Access follows repository artifact permissions;
use a private repository or a separately controlled artifact destination for sensitive
reviewed code and transcripts, and apply your retention policy.
Findings alone retain successful exit status; add `--fail-on-critical` to the review
command if desired. Setup, authorization, stale snapshots and incomplete execution
remain errors. Live provider verification requires separately approved credentials
and budget; ordinary local/CI tests use offline fixtures.
