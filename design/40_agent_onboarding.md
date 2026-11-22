# Agent Onboarding Design

This document specifies end-to-end improvements to the Gitslice agent onboarding
experience: from zero-knowledge discovery through installation, identity
bootstrapping, human-to-agent handoff, workspace orientation, actionable errors,
and zero-install hosted MCP access.

## Goals

1. **Zero-guesswork discovery:** Give coding agents a canonical, progressive-disclosure
   Agent Skill (`SKILL.md`) installable in one command (`gs skill install` or
   `curl -fsSL https://gitslice.io/SKILL.md`) alongside `llms.txt` and targeted
   `gs schema [command]` introspection.
2. **One-shot bootstrap:** Provide `gs onboard` for idempotent agent setup
   (registration or join, optional skill installation, slice access verification,
   and structured JSON handoff instructions) and smooth `install.sh` PATH / skill
   setup.
3. **Single-click human ↔ agent handoff:**
   - **Agent-initiated (`--request-slice`):** Allow `gs auth register-agent` to
     request access to a target slice + role so `/claims` and `gs claims accept`
     can claim ownership and grant slice access in one action.
   - **Human-initiated (`gs auth join`):** Allow a human owner (via Web
     **Connect an Agent** or `gs auth create-agent-invite`) to mint a
     short-lived, single-use join token (`gsi_...`) that creates a co-owned,
     controller-bound agent and grants target slice access in one command — no
     email-matching failures.
4. **Immediate workspace orientation & actionable errors:** Surface projected
   directories and discovered `AGENTS.md`/`CLAUDE.md` files after `gs init`, and
   include machine-readable `remediation_command` and `remediation_url` fields in
   CLI and MCP error payloads.
5. **Zero-checkout hosted MCP:** Expose a stateless Streamable HTTP MCP endpoint
   at `POST /mcp` (proxied at `https://gitslice.io/mcp`) authenticated via the
   agent's `Authorization: Bearer gsk_...` key, reusing `internal/mcpserver` with
   full scoped-key enforcement and scale-to-zero (`Idle When Unused`) compliance.

---

## 1. Official Agent Skill (`SKILL.md`) & `gs skill`

### Distribution

- Canonical file: `web/public/SKILL.md` served at `https://gitslice.io/SKILL.md`
  (with lowercase `/skill.md` redirected 301 to `/SKILL.md` in the Cloudflare
  Worker).
- Embedded copy in `internal/cli/skill_embed.md` (verified identical to
  `web/public/SKILL.md` in unit tests) so `gs skill install` and `gs skill print`
  work offline and in air-gapped environments.

### CLI Commands

- `gs skill print` — writes `SKILL.md` to stdout.
- `gs skill install [--target auto|agents|claude|gemini|cursor|codex|<path>] [--project] [--json]`
  - `--project`: writes to `<cwd>/.agents/skills/gitslice/SKILL.md` (or the
    target harness's project skill directory).
  - Default (`--target auto`, user-global): detects existing harness directories
    (`~/.agents/skills`, `~/.claude/skills`, `~/.gemini/skills`, `~/.cursor/skills`,
    `~/.codex/skills`) and installs into each detected harness; if none exist,
    defaults to `~/.agents/skills/gitslice/SKILL.md`.
- `web/public/install.sh` supports `GS_INSTALL_SKILL=1` (or `--with-skill`) to
  run `gs skill install` right after binary installation, and prints an explicit
  `export PATH=...` hint when `$install_dir` is missing from `PATH`.

---

## 2. Combined Claim + Slice Grant & Single-Use Agent Invites

### 2.1 Agent-Initiated Requested Slice (`--request-slice`)

An agent registering via `gs auth register-agent` often already knows the slice
its human wants it to work on:

```bash
gs auth register-agent \
  --username my-agent \
  --email owner@example.com \
  --request-slice acme/app \
  --request-role writer
```

- **Storage (`agent_registrations`):** Adds `requested_slice_account`,
  `requested_slice_slug`, and `requested_slice_role` (default `"writer"` when a
  slice is specified; allowed values: `reader`, `contributor`, `writer`, `admin`).
  Unauthenticated `RegisterAgent` validates syntax only — it does **not** reveal
  whether a private slice exists.
- **Claims (`ListPendingClaims` / `AcceptClaim`):**
  - `PendingClaim` surfaces `requested_slice` and `requested_role`.
  - `AcceptClaimRequest` adds `bool grant_requested_slice`.
  - When `grant_requested_slice` is true and the claim specifies a slice, the
    server checks that the claiming human has `authz.ActionManageMembers` on that
    slice and grants the requested role to the agent immediately after claiming.
    If the human lacks permission on that slice (or the slice does not exist),
    the claim still succeeds and returns an empty `granted_slice` or clear status
    only when requested — specifically, we validate permission up front when
    `grant_requested_slice` is true so the UI/CLI never silently drops a requested
    grant (or the human can uncheck "Also grant slice access" / pass
    `--no-grant-slice`).
- **Web UI (`/claims` & `PendingClaims`):** Shows the requested slice and role
  with a checked-by-default toggle to approve both ownership and slice access in
  one click.

### 2.2 Human-Initiated Single-Use Join Invites (`gsi_...`)

To eliminate email-verification mismatches completely, a signed-in human user
can mint a short-lived, single-use join token:

- **RPCs (`AuthService`):**
  - `CreateAgentInvite(slice?, role?, suggested_username?, expires_in_seconds?)`
    - Requires a human caller (`kind == "user"`) with an unrestricted session.
    - If `slice` is provided, requires `authz.ActionManageMembers` on that slice;
      `role` defaults to `"contributor"` (allowed: `reader`, `contributor`,
      `writer`, `admin`).
    - Expiry defaults to 24 hours, capped at 7 days.
    - Generates a random `gsi_...` token; stores only its SHA-256 hash in
      `agent_invites`. Returns the plaintext token once.
  - `ListAgentInvites()` / `RevokeAgentInvite(invite_id)`
    - Allows the human to inspect active invites and revoke unused tokens.
  - `JoinAgentInvite(token, username, display_name?)`
    - **Public (unauthenticated)**, protected by the same per-IP signup rate
      limiter (`limiters.signup`) as `RegisterAgent`.
    - Atomically locks and validates the invite row (`for update`: not revoked,
      not consumed, not expired), marks it consumed, creates the agent subject
      with `controller_subject_id = created_by_subject_id`, creates its personal
      account co-owned by both the agent and the inviter, records a claimed
      `agent_registrations` row, grants membership on `slice_id` with
      `slice_role` if set, and issues the agent's initial `gsk_...` API key.
- **CLI:**
  - `gs auth create-agent-invite [--slice account/slice] [--role contributor] [--username name] [--expires 24h] [--json]`
  - `gs auth join [token] [--token -] [--username name] [--display-name name] [--server addr] [--json]`
    (also honors `GS_JOIN_TOKEN` so secrets need not appear in shell history).
- **Web UI (`ConnectAgentPanel`):**
  - Embedded on Slice Settings (pre-bound to that slice for slice admins) and on
    the user's Account page (`OwnedAgents`).
  - Mints an invite and presents three copy-ready outputs:
    1. **Agent Prompt** (ready to paste into Claude Code, Codex, Cursor, or
       Antigravity — installs `gs`, joins via invite, installs `SKILL.md`, and
       checks out the slice).
    2. **CLI Command** (`gs auth join <token>`).
    3. **Hosted MCP Config** (`https://gitslice.io/mcp` JSON config snippet).

---

## 3. One-Shot Idempotent `gs onboard`

```bash
gs onboard --email owner@example.com [--slice acme/app] [--role writer] [--install-skill] [--json]
gs onboard --join gsi_... [--username bot-1] [--install-skill] [--json]
```

- **Idempotent:** If already authenticated (`GetAuthStatus` succeeds) and `--join`
  is not passed, reuses the existing agent identity without creating duplicate
  accounts.
- **Auto-username:** If `--username` is omitted during registration or join,
  derives a valid lowercase slug from the invite's `suggested_username` or local
  hostname + 4 random hex characters.
- **Post-auth checks:** Verifies connectivity, checks access to `--slice` (when
  given), optionally installs `SKILL.md` (`--install-skill`), and outputs a
  structured JSON summary with `account`, `subject_id`, `claim_url`,
  `human_handoff_message`, `slice_access`, `skill_installed`, and `next_commands`.

---

## 4. Workspace Orientation & Actionable Error Remediation

- **Post-`gs init` orientation (`internal/cli/workspace_init.go`):**
  - After hydrating files, scans the workspace's included root paths for common
    agent guides (`AGENTS.md`, `CLAUDE.md`, `GEMINI.md`).
  - Prints discovered guides (`found agent guide: gitslice/gitslice/AGENTS.md`)
    and includes `included_paths`, `slice_ref` (`account/slice`), and
    `agent_guides` in `--json` output.
- **Structured remediation (`internal/cli/errors.go`):**
  - Adds optional `remediation_command` and `remediation_url` fields to CLI JSON
    `errorBody` and MCP tool errors so agents can programmatically recover from
    `unauthenticated`, `not_logged_in`, `permission_denied`, `sync_conflicts`,
    and `not_in_workspace`.
- **Targeted schema discovery (`internal/cli/schema.go`):**
  - Extends `gs schema [command...]` so agents can query a single command family
    (e.g., `gs schema auth` or `gs schema onboard`) instead of parsing the entire
    CLI catalog.

---

## 5. Hosted Streamable HTTP MCP Endpoint (`/mcp`)

- **Transport (`internal/mcpserver/http.go`):**
  - Stateless Streamable HTTP JSON-RPC 2.0 on `POST /mcp` (`Content-Type:
    application/json`).
  - Single requests return `200 OK` with JSON-RPC response body; notifications
    (`id` absent) return `202 Accepted` with empty body.
  - Supports protocol versions `2025-06-18`, `2025-03-26`, and `2024-11-05`.
  - No server-side session state, no SSE keepalive loops, and no background
    timers — fully compliant with `Idle When Unused` (Neon + Cloud Run scale to
    zero).
- **Server wiring (`server/mcp_http.go` & `server/connect.go`):**
  - Mounted at `/mcp` inside `newPrincipalConnectHandler`, inheriting
    `connectPrincipalAuthMiddlewareWithLimiters` (Bearer `gsk_...` / Clerk /
    service-token verification, scoped API key enforcement, per-subject and
    per-IP rate limiting, request IDs, and activity tracking).
  - Unauthenticated calls to `initialize` / `tools/list` / `ping` are allowed
    for client capability discovery; `tools/call` invokes the exact same
    in-process `service.Handlers` with the authenticated `authctx.Principal`, so
    public slices can be read anonymously and private slices / writes require a
    valid key with appropriate slice scope and role.
  - Optional default slice can be passed via `?slice=<account>/<slice>` query
    parameter or `X-Gitslice-Slice` header, or per tool call via the `slice`
    argument.
- **Edge proxy (`web/src/lib/mcpProxy.ts` & `web/src/server.ts`):**
  - Proxies `https://gitslice.io/mcp` (and `/mcp?...`) directly to
    `${VITE_API_BASE_URL}/mcp`, forwarding `Authorization`, `Content-Type`,
    `Accept`, and `MCP-Protocol-Version`.

---

## 6. Security & Idle-When-Unused Considerations

- **Invite token hygiene:** Join tokens (`gsi_...`) are high-entropy random
  secrets stored only as SHA-256 hashes, single-use (consumed atomically inside a
  DB transaction with `FOR UPDATE`), time-bounded (max 7 days), and revocable.
- **No information leakage on unauthenticated signup:** `RegisterAgent` stores
  `requested_slice` as opaque strings without querying private slices.
  `JoinAgentInvite` returns a uniform error for invalid, expired, revoked, or
  already-used tokens.
- **Scoped API key enforcement:** `CreateAgentInvite`, `ListAgentInvites`,
  `RevokeAgentInvite`, and `AcceptClaim` require unrestricted human credentials;
  hosted `/mcp` enforces all existing API key slice scopes and role caps via
  `authctx.Principal`.
- **Idle When Unused:** Zero background sweeps or timers are added. Expired
  invites are simply rejected on read/join.
