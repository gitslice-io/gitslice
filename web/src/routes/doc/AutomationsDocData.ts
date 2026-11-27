export const fieldRows: Array<[string, string]> = [
  [
    "description",
    "Short summary shown in the folder policy drawer, Run modal, and CLI.",
  ],
  [
    "on",
    "Events that start the automation: push, tag.created, changeset.created, changeset.updated, changeset.approved, changeset.submitted, changeset.abandoned, check_run.completed, manual, or call (reusable template invoked via action.uses).",
  ],
  [
    "paths",
    'Globs relative to the folder, "/" for the account root, "!" to exclude. Default: any change under the folder.',
  ],
  [
    "changes",
    "Filter by change kind: added, modified, deleted, moved. Default: any.",
  ],
  ["refs", "Target refs such as main. Default: any."],
  [
    "inputs",
    "Typed manual inputs (type: string | choice | boolean | number, description, required, default, options, pattern). Passed as event.inputs (worker) or INPUT_<NAME> env vars (bash).",
  ],
  [
    "manual",
    "Manual invocation policy: roles ([admin, writer] or [admin]), require_human (true blocks agent accounts), confirm (prompt shown before running).",
  ],
  [
    "secrets",
    "Folder secrets the automation may use (list [FOO] or slot mapping { TEMPLATE_SLOT: CALLER_SECRET } for action.uses). Sealed as opaque placeholders by default in worker and http-gate bash runs.",
  ],
  [
    "reveal",
    "Subset of secrets exposed in plaintext (via gs.revealSecret(name) in workers, or $NAME and $GITSLICE_SECRETS_DIR/<NAME> in bash). Required for secrets used under egress: direct.",
  ],
  [
    "network",
    "Allowlist of public HTTPS hosts (such as api.github.com or *.cloudflare.com) permitted through Gate for worker and http-gate bash automations.",
  ],
  [
    "egress",
    "Bash actions only: offline (default when network is empty), http-gate (default when network is set; sealed secrets unwrapped at egress), or direct (full TCP/UDP internet for psql, ssh, aws, gcloud; requires reveal).",
  ],
  [
    "checks",
    "Check names the automation may report. Each becomes <folder>/<name> and can be marked required on a slice.",
  ],
  [
    "concurrency",
    "cancel-superseded (default for changeset events), serial, or parallel.",
  ],
  [
    "concurrency_group",
    "Worker and bash actions: a name shared with the account's other automations (such as prod-server for deploy and rollback). No two runs in one group run at once; the others wait their turn, oldest first. Implies serial.",
  ],
  [
    "timeout",
    "Maximum run duration. Default 5m (max 15m) for bash actions; default 1h (max 7d) for worker/check runs.",
  ],
  [
    "retry",
    "Retry an interrupted worker or bash run up to 5 times. Default 0 (at most once).",
  ],
  [
    "action",
    "http (url, headers, sign), worker (.js/.mjs module under .gitslice/), bash (Cloudflare Container shell run/script), check (checks from checks.yaml), uses (call an on:[call] reusable template), or steps (sequential multi-step pipeline).",
  ],
];

export const bashRuntimeRows: Array<[string, string]> = [
  [
    "base (default)",
    "Debian slim with bash, curl, jq, git, openssh-client, openssl, zstd, and gs-auto.",
  ],
  ["node", "base + Node.js 22 LTS, npm, and pnpm."],
  ["python", "base + Python 3.12 and uv."],
  ["go", "base + Go 1.24 toolchain."],
  [
    "cloud",
    "base + gcloud, aws, kubectl, psql, gh, and terraform for deploy and ops runbooks.",
  ],
];

export const apiRows: Array<[string, string]> = [
  [
    "onEvent(event, gs)",
    "Entry point called once when the worker automation starts.",
  ],
  [
    "onCallback(request, gs)",
    "Called for each HTTP POST to the run's callback URL until the run finishes.",
  ],
  [
    "gs.checks.create / .update",
    "Report check runs with status, summary, detailsUrl, and file annotations (path, line or startLine/endLine, level, message).",
  ],
  [
    "gs.files.read(path) / .readText(path) / .list(dir)",
    'Read files at the changeset patchset (default or { ref: "patchset" }), at its base ({ ref: "base" }), or at the current landed target ref ({ ref: "main" }); { at } is accepted as an alias for { ref }.',
  ],
  [
    "gs.changeset.get()",
    "Fetch the changeset, its patchsets, and approvals with resolved usernames and attribution.",
  ],
  [
    "gs.changeset.diff(opts?)",
    "Fetch the unified diff for the run's patchset (optional { paths, fromPatchset }, account-scoped, capped at 4 MiB).",
  ],
  [
    "gs.secret(name)",
    "Returns an opaque placeholder that Gate swaps for the real secret value on outbound requests to allowed network hosts. Put it in a URL's path or query, a header, or the body; it cannot be the whole URL, since a secret may never decide where a request goes.",
  ],
  [
    "gs.revealSecret(name)",
    "Returns the raw value for a secret listed in reveal when script code must compute with it directly.",
  ],
  [
    "gs.callbackUrl(data)",
    "Generate a signed callback URL where an external system can POST results back to this run.",
  ],
  [
    "gs.waitForCallback()",
    "Keep the run open after the current call returns, until gs.finish(), gs.fail() or the timeout.",
  ],
  [
    "gs.finish()",
    "End the run as succeeded (or by its checks' results) once the current call returns.",
  ],
  [
    "gs.fail(message)",
    "End the run as failed with your own message, without a stack trace, once the current call returns.",
  ],
  [
    "gs.setDetailsUrl(url)",
    "Link the run to an https page outside Gitslice, such as the build it started; gs auto show and --follow print it.",
  ],
  [
    "gs.setOutput(key, value)",
    "Emit a named output on the run (secret-masked) for downstream pipeline steps (${{ steps.<id>.outputs.<key> }}) and inspection.",
  ],
  [
    "gs.state.get() / .set(value)",
    "Persist up to 64 KiB of JSON state across callbacks within the run.",
  ],
  [
    "gs.idempotencyKey(label)",
    "Deterministic key that stays identical across retries of the same run.",
  ],
  ["console.log", "Writes to the run's log with secret values masked as ***."],
];
