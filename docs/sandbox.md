# Linux external sandbox

`qual-sandbox` applies an operating-system boundary before executing the selected
Qual binary. Qual remains one Go binary. The optional launcher uses isolated-mode
Python 3 for preparation, bubblewrap for mounts/namespaces, and the official
Anthropic sandbox runtime for restricted network access. It never executes Qual
to probe its version or discover its configuration outside the boundary.

Install trusted distribution packages `bubblewrap` (0.8 or newer), `python3`,
`git`, `socat`, `ripgrep` and `which`. Restricted mode also needs Node.js 22.12 or
newer. Install the locked JavaScript dependencies from this checkout:

```sh
npm ci --ignore-scripts --prefix sandbox
scripts/qual-sandbox --help
```

The dependency is **`@anthropic-ai/sandbox-runtime` 0.0.83**, from
[anthropics/sandbox-runtime](https://github.com/anthropics/sandbox-runtime/tree/v0.0.83).
`sandbox/package-lock.json` pins its complete dependency graph with registry URLs
and SHA-512 integrity hashes. Installation is an explicit administrator action;
the launcher does not download packages, run package scripts, or update itself.
The installed runtime and selected tool executables must be trusted. Qual itself
and the input repository may be untrusted. Python uses `-I` so project modules,
`PYTHONPATH`, and user site packages cannot replace preparation code. The only
host executable started during preparation is system bubblewrap from
`/usr/bin:/bin`; repository entries on `PATH` cannot replace it. Node, ripgrep,
which and the selected Qual executable are located through the caller's trusted
`PATH`, resolved, and mounted read-only. System socat must be installed under
`/usr/bin` or `/bin`.

For a local checkout, use a new output directory outside the source and its Git
storage. Select every network endpoint explicitly, including its destination
port:

```sh
scripts/qual-sandbox --input /path/to/repo --output /path/to/new-review-output \
  --qual /path/to/qual --network restricted \
  --allow api.openai.com:443 --allow github.com:443 \
  -- review . --staged
```

Omit `--input` for a URL review. The workspace starts empty and cloning happens
inside the boundary:

```sh
scripts/qual-sandbox --output /path/to/new-review-output --network restricted \
  --allow api.openai.com:443 --allow github.com:443 --allow api.github.com:443 \
  -- review https://github.com/org/repo/pull/123
```

Arguments after `--` are passed unchanged. Local paths in those arguments refer
to the isolated filesystem: use `.` for the copied checkout. No shell evaluates
those arguments. Progress goes to stderr; Qual keeps stdout for command output.
The launcher prints the network mode, mount destinations, report path, deadline,
and absence of CPU/memory limits without printing credentials or the invocation.

## Files and credentials

The filesystem starts empty rather than binding the host root. The mount profile
is readable in `scripts/qual-sandbox`:

| Inside | Access and origin |
| --- | --- |
| `/workspace` | Writable independent byte copy of the selected checkout, including ignored/untracked files, selected index, objects, refs and worktree metadata |
| `/state` (`QUAL_HOME`) | Writable separate state under the output directory |
| `/output` | Writable `OUTPUT/reports`; direct Qual reports here through its normal output options |
| `/context` | Read-only explicitly selected rules |
| `/home/qual` | Empty private home; the real home and main `QUAL_HOME` are absent |
| `/tmp`, `/run`, `/dev`, `/proc` | Private temporary storage, minimal devices and a private PID view |
| `/opt/qual-tools/qual` | Read-only selected executable |
| `/opt/qual-sandbox` | Read-only launcher-owned runtime and pinned packages, in restricted mode |
| `/usr/bin`, `/usr/lib`, `/usr/lib64` | Read-only trusted system programs and libraries |
| CA certificates, Git support files, loader/resolver files | Read-only selected system paths |

The original checkout and Git storage are never mounted. Copying uses no Git
commands, hooks, filters or installation scripts; it creates no hardlinks.
Linked worktrees are flattened into independent Git storage with the selected
worktree's own HEAD, index, private refs and reflogs. Source symlinks remain
symlinks and cannot expose unmounted host paths. Special files/sockets in source,
symlinks in Git metadata, and Git object alternates fail closed. Materialize
alternates with trusted Git tooling first. Do not modify the input concurrently
with preparation: the copy is independent but is not an atomic host filesystem
snapshot. Git configurations that refer to additional absolute host paths need
to be made portable before use; those paths remain inaccessible.

Only a small fixed environment is inherited. No ssh-agent, Docker, D-Bus or
management sockets are mounted, and non-stdio inherited file descriptors are
closed. A selected toolchain directory may be added with `--toolchain DIR`;
it appears read-only at `/opt/toolchains/N`, with its `bin` directory appended to
`PATH`. Toolchains must be trusted and must not contain sockets or special files.
They must work at their isolated paths. System directories and administrator
selected toolchains are part of the trusted computing base.

Select user configuration and credentials explicitly:

```sh
scripts/qual-sandbox --input /path/to/repo --output /path/to/new-review-output \
  --network restricted --allow api.openai.com:443 \
  --settings /path/to/selected-settings.yaml --rules /path/to/selected-rules.md \
  --credential OPENAI_API_KEY=/path/to/owner-only-key-file \
  -- review .
```

Settings are copied to `/state/settings.yaml`; selected rules are available at
`/context/rules.md`. Choose the corresponding Qual rule/output options when
needed. The launcher does not merge or import the real `QUAL_HOME`. Credential
files must be caller-owned, owner-only regular files containing a single value.
Selected names must end in `_API_KEY` or `_TOKEN`; environment-control names such
as `BASH_ENV` are rejected. Values enter the clean child environment, never
launcher arguments, diagnostics or the manifest. Publication credentials are not
selected by default. Do not put secrets in Qual command-line arguments: the
invocation is retained privately as run evidence.

All selected credentials and source files are available to every process inside
the shared sandbox. A hostname allowlist does not constrain operations using an
API key, prevent sending data to an allowed endpoint, or cap provider billing.
Use remote permissions and provider quotas for those controls. Agents within one
sandbox are not isolated from each other.

## Network modes

- `none` is the default. Bubblewrap creates a private network namespace without
  an outbound route or proxy. Direct TCP, UDP and DNS cannot leave it.
- `restricted` first applies the strict filesystem/PID boundary. A trusted Node
  parent then starts the pinned runtime's official proxies and uses its nested
  bubblewrap network/PID namespace and seccomp profile before Qual starts.
  Only the official controlled HTTP/SOCKS channel reaches the parent network.
  Removing proxy variables does not restore a route; direct TCP/UDP/DNS bypasses
  remain blocked. DNS for proxied destinations is resolved by the trusted proxy.
  `--allow HOST:PORT` accepts exact endpoints, without wildcards or implicit
  default permissions. A self-hosted model can be selected with an explicit
  address and port; this does not grant general host loopback access. HTTP
  clients may need to override their `NO_PROXY` handling for a selected local
  endpoint: the direct connection remains blocked.
- `open` must be selected explicitly. It shares the host network and permits
  arbitrary egress, including host/LAN services. A diagnostic states this
  exposure. Filesystem, environment, PID and IPC isolation still apply.

A missing backend, missing pinned dependency, failed proxy startup, unsupported
profile or denied namespace syscall fails the launch. There is no direct-exec or
open-network fallback. Linux hosts must permit the required nested user and
network namespaces. Container deployments must allow those operations explicitly;
ordinary unprivileged jobs on hosts that disable them cannot run this recipe.

The mechanism sources are [bubblewrap](https://github.com/containers/bubblewrap)
and [sandbox-runtime's Linux implementation](https://github.com/anthropics/sandbox-runtime/tree/v0.0.83/src/sandbox).
The Qual profile is the allowlist in the launcher, not upstream default filesystem
access or an `HTTP_PROXY` environment variable by itself.

## Results, cancellation and verification

The output directory must not already exist or be a symlink; it is created mode
0700. Once preparation begins it retains
`reports/`, `.qual-sandbox/state`, the independent source copy, private invocation
and `sandbox.json` after success, failure, timeout or cancellation. Treat reports
and state as untrusted data, and do not automatically import them into the main
`QUAL_HOME`. The manifest contains network policy and timeout, never credentials.
The launcher forwards SIGINT/SIGTERM and kills the sandbox tree at the execution
deadline (`--timeout SECONDS`, default 3600). Detached descendants remain in the
private PID namespace and die with it. CPU and memory resource limits are not
applied; deployments may add explicit outer resource limits. Preparation is a
regular file copy and is interruptible, not an atomic operation.

Run the offline real-process boundary tests after installing the dependencies:

```sh
tests/sandbox/run.sh
```

They use temporary real repositories, synthetic secrets, real Unix sockets,
responding local network fixtures and hostile helper executables. They cover
source/index independence, linked worktrees, home/main-state secrecy, descriptor
closure, environment/startup injection, read-only tools/profile, exact proxy
host/port policy, direct network bypass with proxy variables removed, descendants,
timeout, unavailable dependencies, and absence of an unsafe fallback. No provider
credentials or live model calls are used. A test failure caused by disabled host
namespace/socket operations is a failed environment prerequisite, never a skipped
security assertion.
