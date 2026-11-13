# Sandbox dependency advisory assessment

Assessed 2026-11-13, pending independent review. The locked Linux proxy runtime is
`@anthropic-ai/sandbox-runtime@0.0.83` with `zod@3.25.76`. The npm registry still
identifies 0.0.83 as the latest runtime, depending on `zod ^3.25.0`.

`npm audit --prefix sandbox` reports **two high-severity package findings from one
advisory**, [GHSA-mrhx-c7xc-m3w5](https://github.com/advisories/GHSA-mrhx-c7xc-m3w5)
(CVE-2023-54404): oversized invalid collections can exhaust memory during Zod
validation. The advisory marks versions below 4.6.6 affected; 4.6.6 is patched.
The two package entries are Zod itself and sandbox-runtime through Zod. The audit
is not clean and the recipe must continue to report this finding.

The runtime deliberately imports `zod/v3`, even when Zod 4 supplies that compatibility
path (`dist/sandbox/sandbox-config.js:6-10` and
`dist/sandbox/decider-client.js:51`). Inspection of the published 4.6.6 archive shows
its v3 `ZodArray._parse` implementation remains the same as 3.25.76, apart from line
shifts; a major override would not demonstrate remediation of this selected v3
validator. We have not changed the approved runtime pin or hidden the advisory.

For the selected Qual Linux integration, static source inspection finds no route
from hostile source code or proxy request bodies into a Zod collection validator:

- `sandbox/restricted.mjs` invokes the `SandboxManager` library with a fixed profile,
  explicit launcher-selected allow entries, and no project-supplied configuration.
  Generated GitHub/GitLab workflows supply three/two constant egress entries. Target
  text never appends allow entries or changes this profile.
- `SandboxManager.initialize` accepts that object directly
  (`dist/sandbox/sandbox-manager.js:813`, `initializeSteps` at 856), rather than
  invoking the CLI's schema loader.
- Zod configuration `safeParse` calls occur in
  `dist/utils/config-loader.js:13,57`, selected by `dist/cli.js`. Qual does not call
  that CLI or loader.
- Other Zod `safeParse` calls are in `dist/sandbox/decider-client.js:252,674`,
  imported by the separate `dist/sandbox/proxy-only.js` integration. Qual does not
  select that proxy-only/decider path. Its `SandboxManager` HTTP/SOCKS proxy path
  does not parse attacker-provided collections with these schemas.

This is a narrow reachability assessment of the known dependency denial-of-service
advisory, not a claim that the package is patched, a resource-limit guarantee, or
an exception to any filesystem/network boundary check. All 23 real-process Linux
boundary tests passed with the unchanged locked dependencies. Continue monitoring
upstream for a supported fix. Reassess before introducing project-provided sandbox
configuration, upstream CLI/control-FD configuration, proxy-only deciders, or a new
runtime pin. Reviewers should independently confirm this reachability reasoning.
