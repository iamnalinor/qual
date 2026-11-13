# Install the GitLab trusted-ref review pipeline

Save the YAML on the protected default branch (or include it from a protected
configuration project pinned by commit). Do not expose model credentials to ordinary
source-branch or merge-request pipelines. The workflow accepts only `trigger` and
manual `web` pipelines on the protected default ref. Keep project variables, runner
configuration, pipeline YAML and the selected Qual source under trusted administration.

Build a trusted Linux image containing Go 1.27.4, Node >=22.12.0, bubblewrap, Python3,
socat, ripgrep, which, git, curl and CA certificates. For example, copy `/usr/local/go`
from `golang:1.27.4-bookworm` into `node:22.12.0-bookworm`, add `/usr/local/go/bin` to
PATH, and install the listed Debian packages. Pin both base images and the resulting
image by digest in your deployment. Set `QUAL_CI_IMAGE` to that image digest. Configure
a dedicated `qual-sandbox` runner that permits nested user/PID/network namespaces;
Ubuntu hosts may require `kernel.apparmor_restrict_unprivileged_userns=0`. The runner
must support bubblewrap's boundary; a namespace denial fails review without fallback.
Do not run this credential-bearing pipeline on a shared untrusted shell executor.

Set protected `QUAL_SOURCE_SHA` to the full audited 40-character Qual commit including
working `ci authorize` and review snapshot guards. Set protected masked
`QUAL_SOURCE_READ_TOKEN` to a read-only GitHub token for the template's authenticated
source download. The default source repository, `iamnalinor/qual`, is public; a
private fork requires a token authorized for that fork. The pinned archive is fetched
through its fixed GitHub API endpoint. No published release is assumed. Configure
protected `GITLAB_TOKEN` with read_api/read_repository access for your instance. It resolves metadata, target
branch commits and the initiator's membership; it cannot publish review comments.
The authorization job uses no model credentials and transfers only its own trusted
binary, launcher/dependencies and decision through a one-hour pipeline artifact.

Create a protected environment `qual-review`. Scope protected masked `OPENAI_API_KEY`
and protected file variable `QUAL_CI_SETTINGS` exclusively to that environment, so
they are absent from the authorization job. Generate settings with your version's
`qual init`/configuration schema and select provider/model, explicit budget/hard limit,
environment credential references and untrusted project behavior. Settings are trusted
file-variable data, never source-MR YAML. Set the exact `api.openai.com:443` route or
edit model/settings/credential/egress together for another provider. Protect deployment
permissions for this environment. No provider/billing fallback or publication exists.

In GitLab Settings > CI/CD > Pipeline trigger tokens, create a token owned by the
trusted automation identity. In Settings > Webhooks, configure the documented trigger
endpoint for your GitLab instance:

`https://GITLAB_HOST/api/v4/projects/PROJECT_ID/ref/ENCODED_DEFAULT_BRANCH/trigger/pipeline?token=TRIGGER_TOKEN`

Enable **Merge request events** and **Comments** for that project. GitLab writes the
original request into the `TRIGGER_PAYLOAD` file variable. The URL token is a secret;
configure it in GitLab, never paste a populated URL into YAML, commits, issue text or
logs. Restrict access to webhook/trigger administration. This bearer-authenticated
endpoint trusts the webhook sender's actor assertion; anyone with the trigger token
could forge payloads. Rotate on exposure. The pipeline trigger route is essential:
GitLab does not automatically turn MR comments into native CI pipeline events.
Normal MR pipelines and arbitrary trigger refs are rejected by workflow rules.

Manual alternative: Run pipeline on the protected default branch, with
`QUAL_MR_NUMBER` set to the numeric MR IID. Actor ID comes from trusted
`GITLAB_USER_ID`. For webhooks it comes from `user.id` in the secured delivery, checked
against project membership API. Developer (30), Maintainer (40), and Owner (50)
access qualifies. Open/reopen/update/ready requests qualify; label additions require
exactly `qual-review`, and MR comments require exactly `/qual review`. Draft and
closed MRs, unrelated labels, malformed/foreign project payloads and unauthorized
initiators stop before the review job receives model credentials.

`CI_API_V4_URL` and `CI_PROJECT_ID` come from the trusted runner context; never use a
payload-supplied endpoint or project. For self-managed GitLab, use its normal API URL
and change the sandbox's explicit `gitlab.com:443` allow entry to your exact instance
host/port. Captured target T and source H SHAs are passed as review guards; either
moving causes an explicit stale-snapshot failure before any model call. Diff base
remains merge-base(T,H). Webhook bodies are never interpolated as shell code.

Markdown/JSON reports and session traces are retained seven days, including failure.
The template limits artifact UI/API access to Developer or higher. Set project CI/CD
visibility to Only project members to constrain job-token artifact access too; see
[GitLab artifact access](https://docs.gitlab.com/ci/yaml/#artifactsaccess). Reviewed code/transcripts
can be sensitive. Findings alone are successful; add `--fail-on-critical` for a policy
failure. Setup, authorization, stale and incomplete outcomes stay errors. Live API/
provider verification is separate and requires approved credentials and budget;
ordinary tests use offline fixtures. The required final acceptance is an executable
CLI + sandbox + fixture-provider job, not template syntax alone.
