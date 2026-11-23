# 16. Bring Your Own Agent (BYOA)

## Goal

Let a user run their own coding agent on their own machine and drive it from the
Gitslice web UI (or use the same daemon as a checks-only CI runner). The user
runs `gs agent start` in an empty directory (or `gs agent start --ci` for
checks-only in any directory); that process becomes a long-lived **agent
daemon** (`internal/agentd/`). From the **Agents** view of a slice, the user can
open one or more **conversations** with that daemon. Each conversation runs the
agent (initially Codex via `codex app-server` JSON-RPC) inside its own `gs`
workspace sub-directory bound to the slice. Conversation traffic is streamed
between the browser and the daemon, relayed by the central Gitslice server.

For agents that connect directly to Gitslice as an MCP tool server rather than
through the BYOA daemon relay, see `gs mcp` (`internal/mcpserver/`).

## Why a relay

The daemon runs on the user's laptop or build host, behind NAT, with no inbound
connectivity. It therefore cannot be a server the browser dials. Instead the
daemon holds a single persistent **outbound bidirectional gRPC stream** to the
central server, and the server acts as a **hub** that routes messages between
browser and daemon, keyed by conversation id.

```text
Browser ── unary SendAgentMessage ─▶ Server ── ServerMessage (bidi) ─▶ Daemon ─▶ codex app-server
Browser ◀── StreamConversation (SSE) ─ Server ◀── DaemonMessage (bidi) ─ Daemon ◀─ codex app-server
```

All conversations (and CI check runs) for one daemon multiplex over that
daemon's single `Connect` stream, tagged by `conversation_id` / run id.

## Decisions

- **Daemon scope: account-wide (optionally slice-restricted).** A daemon belongs
  to the subject that started it. Each *conversation* binds to exactly one slice
  (preserving the one-workspace-one-slice rule). `--slice <account>/<slice>`
  (repeatable) limits conversations on that daemon to specific slices.
- **Conversation roles (`reader`, `contributor`, `writer`).** Each conversation
  is started with a `role` (default `contributor`), validated against the
  caller's authority on the slice. The daemon mints a scoped child API key for
  that conversation matching its single slice and role (see "Per-conversation
  scoped keys" below).
- **Public-slice conversation reads.** Conversations on public slices (including
  transcripts linked from changeset patchsets) are readable without
  authentication via `GetConversation`, `ListConversations`, and
  `GetConversationEvents`.
- **Web receive transport: server-streaming RPC via ConnectRPC**
  (`StreamConversation`), consumed in the browser through the generated
  Protobuf-ES service descriptor and Connect web transport. Web send is a unary
  RPC.
- **Daemon auth & per-conversation keys.** The daemon authenticates with its
  saved `gs` credential (ideally a scoped `gsk_` API key limited to the daemon's
  slices; `gs agent doctor` warns when running on a full interactive sign-in).
  For each conversation, `internal/agentd/daemon_keys.go` mints a **12-hour
  per-conversation scoped child key** restricted to that conversation's slice
  and role.
- **Agent runtime: `codex app-server` JSON-RPC** (`internal/agentd/codex_rpc.go`,
  `runtime.go`) behind a small `Runtime` interface so other runtimes can be
  added later.
- **In-process workspace operations (`HostCallbacks`).** Rather than shelling
  out to `gs` subprocesses for workspace init and turn capture, `agentd.Daemon`
  invokes in-process `HostCallbacks` (`WorkspaceInit`, `CaptureChangeset`,
  `Dial`, `ObjectCache`, `FetchCheckBatch`, `CheckTreeWriter`) wired by the CLI.
- **Durability: every conversation event is persisted** to Postgres with a
  per-conversation monotonic `seq`. History reloads from the DB; a reconnecting
  web stream replays from `after_seq` then tails live.

Server→daemon user-message delivery is durable across a missing or half-open
`Connect` stream. `DeliverUserMessage` carries the persisted user event `seq`;
after the daemon reports `ConversationStarted` during reconnect replay, the
server resends every trailing user message with no later durable non-user event.
The daemon serializes turns and deduplicates positive seqs, while legacy seq 0
messages still run every time. Daemon runtime errors for known conversations use
the existing client-sequenced ack/resend buffer as well.

## Components

### Proto: `AgentService` (`proto/core/v1/agent.proto`)

Daemon side:

- `rpc Connect(stream DaemonMessage) returns (stream ServerMessage)`
  First `DaemonMessage` must be `RegisterDaemon{name, runtime, version}`; server
  replies `DaemonRegistered{daemon_id}`. Thereafter the daemon sends `Heartbeat`,
  `AgentEvent` (streamed runtime output for a conversation), and
  `ConversationStarted` (workspace ready). The server sends `StartConversation`,
  `DeliverUserMessage`, `CancelConversation`, `CloseWorkspace`,
  `ReconcileWorkspaces`, and `Ping`.
- `rpc StandbyPoll(StandbyPollRequest) returns (StandbyPollResponse)`
  Used by detached `--ci` daemons to stay registered/online without holding an
  open `Connect` stream so the server and database can scale to zero.

Web side (unary + one server-stream, surfaced through ConnectRPC):

- `rpc ListDaemons(ListDaemonsRequest) returns (ListDaemonsResponse)`
- `rpc RenameDaemon(RenameDaemonRequest) returns (Daemon)`
- `rpc CreateConversation(CreateConversationRequest) returns (Conversation)`
- `rpc ListConversations(ListConversationsRequest) returns (ListConversationsResponse)`
- `rpc GetConversation(GetConversationRequest) returns (Conversation)`
- `rpc GetConversationEvents(GetConversationEventsRequest) returns (GetConversationEventsResponse)`
- `rpc CloseConversation(CloseConversationRequest) returns (Conversation)`
- `rpc SendAgentMessage(SendAgentMessageRequest) returns (SendAgentMessageResponse)`
- `rpc StreamConversation(StreamConversationRequest) returns (stream ConversationEvent)`

### Server hub (`service/agent.go`, `service/agent_connect.go`, `service/agent_hub.go`, `service/agent_standby.go`)

In-memory registry:

- `daemon_id → daemonConn` (a send channel to the daemon's `Connect` stream).
- `conversation_id → set of subscriber channels` (live web `StreamConversation`
  calls).

Flow:

1. Daemon `Connect` → authenticate subject → upsert `agent_daemons` row
   (status `online`) → register `daemonConn` in the hub. On stream end, mark
   `offline` and drop the entry (unless the daemon detached cleanly into
   `StandbyPoll`).
2. Web `CreateConversation{daemon_id, slice, role}` → authz (subject owns daemon
   & holds at least the requested `role` on the slice) → insert
   `agent_conversations` row → if daemon online, push `StartConversation` over
   its stream.
3. Web `SendAgentMessage{conversation_id, text}` → persist a `user` event →
   push `DeliverUserMessage` to the daemon.
4. Daemon `AgentEvent` → persist event (assign `seq`, preserving `item_id` when
   present) → fan out to all live `StreamConversation` subscribers for that
   conversation. Runtime token deltas are persisted too; clients use
   `type + item_id` to coalesce them with the finalized item.
5. Web `StreamConversation{conversation_id, after_seq}` → replay persisted
   events with `seq > after_seq`, then subscribe to the live hub topic.

The hub keeps no agent output of its own; it is a stateless relay plus the
Postgres log. The daemon is the only place agent code runs.

### StandbyPoll & Cloudflare Doorbell (`service/doorbell.go`, `internal/agentd/doorbell.go`, `internal/agentd/daemon_standby.go`)

A checks-only daemon (`gs agent start --ci`) can detach from `Connect` after
`--detach-after` (default `agentd.DefaultDetachAfter`, env
`GITSLICE_AGENT_DETACH_AFTER`; `0` disables detaching) so an idle Cloud Run
server and database can scale to zero:

- When idle, the daemon closes `Connect` cleanly and enters `StandbyPoll`.
- `StandbyPollResponse` returns a signed `doorbell_url` / token (3-hour token
  lifetime, HMAC-signed with a shared secret) for the Cloudflare Doorbell Worker
  (`doorbell/`, see [17_continuous_integration.md](17_continuous_integration.md)).
- The daemon holds a lightweight WebSocket open to the Doorbell Worker
  (`internal/agentd/doorbell.go`) and only issues a safety `StandbyPoll` once
  per hour (`doorbellSafetyPoll`, with a 3-minute `doorbellStandbyGrace` window
  for online status).
- When a check run is queued for that daemon, the server rings the Doorbell
  Worker (`service/doorbell.go`), waking the daemon to reconnect `Connect` and
  claim the work immediately. Because Postgres remains the durable queue, a lost
  ring only delays pickup until the next safety poll.

### Data model (`internal/postgres/migrations/0014_agents.sql` + subsequent migrations)

- `agent_daemons(id pk, subject_id, account, name, label, runtime, version,
  status, last_seen_at, created_at)`
- `agent_conversations(id pk, daemon_id fk, subject_id, slice_id, account,
  slice_name, role, title, status, workspace_subdir, next_seq, created_at,
  updated_at)`
- `agent_conversation_events(id pk, conversation_id fk, seq, role, type, text,
  data_json, item_id, client_seq, created_at, unique(conversation_id, seq))`
- `daemon_slice_exposures(daemon_id, slice_id, exposed_by)` (migration `0038`)

### Daemon package (`internal/agentd/`) & CLI (`internal/cli/agent*.go`)

The daemon implementation lives in `internal/agentd/` (extracted from
`internal/cli/agent.go`), separating daemon runtime, check execution, standby
polling, doorbell, key management, doctor diagnostics, and service installation
from CLI command wiring:

- `daemon.go`, `daemon_turns.go`, `daemon_events.go`, `daemon_workspaces.go`,
  `daemon_watch.go`: core daemon loop, `HostCallbacks`, workspace hydration,
  turn serialization, and event buffering.
- `codex_rpc.go`, `runtime.go`, `instructions.go`, `protocol.go`: `Runtime`
  interface and `codex app-server` JSON-RPC client (`thread/start`,
  `thread/resume`, streaming turns).
- `daemon_keys.go`: 12-hour per-conversation scoped child key minting,
  refresh, revocation, and isolated `run-homes/<conversation_id>/` environment
  construction (`HOME`, `XDG_DATA_HOME`, `GS_TOKEN`).
- `daemon_standby.go`, `doorbell.go`: detached `--ci` standby polling and
  Cloudflare Doorbell WebSocket listener.
- `checks*.go`, `daemon_checks*.go`: CI check materialization, streaming logs,
  and outbox reporting.
- `doctor.go`, `install.go`: environment/isolation/disk diagnostics and
  OS service installation (`systemd` / `launchd`).

CLI commands (`gs agent`):

- `gs agent start [--name N] [--runtime codex] [--slice account/slice]... [--ci] [--detach-after D] [--lazy] [--idle-timeout D]` — starts the daemon. In
  agent mode, must be run in an empty directory; each conversation gets
  `./conversations/<conversation_id>/` (eager checkout or `--lazy` FUSE mount,
  falling back to eager if FUSE is unavailable). In `--ci` mode, runs checks
  only with no agent runtime in any directory.
- `gs agent create <name> [--slice account/slice] [--role reader|contributor|writer|admin] [--display-name N]` — creates a delegated agent owned and controlled
  by the signed-in user (see [12_account_auth.md](12_account_auth.md)).
- `gs agent status [--slice account/slice]` / `gs agent stop` — inspect or stop
  daemons.
- `gs agent rename <daemon> [new-name] [--clear]` — set or clear the daemon's
  display name (`agent_daemons.label`).
- `gs agent expose <daemon> <slice>` / `unexpose <daemon> <slice>` — manage
  slice CI exposure.
- `gs agent doctor [--ci]` — checks auth, runtime availability, disk space, and
  agent isolation (warning when running on a full interactive sign-in instead of
  a scoped daemon key).
- `gs agent install [--ci] [--name N] [--slice account/slice]... [--print]` /
  `gs agent uninstall [NAME]` — install or remove a background OS service.

### Per-conversation scoped keys (`internal/agentd/daemon_keys.go`)

When a conversation starts (or before a turn if the current child key is near
expiry), the daemon mints a **12-hour child API key** scoped to:

- `slice_ids = [conversation.slice_id]`
- `max_role = conversation.role` (`reader` | `contributor` | `writer`)
- `daemon_id` and `conversation_id` metadata for audit attribution

The child key is passed to the coding agent process as `GS_TOKEN`, with `HOME`
and `XDG_DATA_HOME` pointed at an isolated `run-homes/<conversation_id>/`
directory containing no saved `config.json`. Closing a conversation revokes its
child key.

### Web: Agents page (`web/src/routes/SliceAgentsPage.tsx` + components)

Agents is a per-slice route, `/slices/$account/$slice/agents` (and
`/slices/$account/$slice/agents/$conversationId`), currently feature-gated by
`conversationsEnabled` (`web/src/lib/features.ts`). When enabled, it lists the
subject's online daemons and the slice's conversations with a streaming chat
panel (`AgentsTab.tsx`, `AgentConversation.tsx`). Public slices allow
unauthenticated read-only viewing of conversations.

## Naming and seeing daemons

A daemon registers under the name it was started with (`gs agent start --name`)
and the account of the identity that started it; a restart with the same name
finds the same daemon row, so slices keep pointing at it. That name is its
identity, so renaming cannot change it. Instead a daemon has a **display name**
(`agent_daemons.label`), set by `RenameDaemon` and shown wherever the daemon is
named: `gs agent status`, the web app, a slice's CI daemon (`Slice.ci_daemon_name`)
and the check runs it ran (`CheckRun.daemon_name`). Both are joined in when the
slice or run is read, so a rename shows everywhere at once and to people who
cannot list the daemon. Clearing the display name shows the registered one again.

`ListDaemons` returns the daemons the caller registered (`mine`) and those
registered under accounts the caller owns or administers. That is how a person
who has claimed an agent ([12_account_auth.md](12_account_auth.md)) sees the
agent's daemons, including in the CI daemon picker. Seeing a daemon grants
renaming it, choosing it as a slice's CI daemon, and seeing which readable
slices use it. It does **not** grant conversations on it: `CreateConversation`
still needs the caller to have registered the daemon, so conversation pickers
offer only `mine` daemons.

Display names are 1 to 64 characters without control characters, and unique
among an account's daemons, compared without regard to case, against both
display names and registered names (`AlreadyExists` otherwise). Anyone who may
not manage a daemon gets `NotFound`, so ids reveal nothing.

A daemon runs the checks of every slice that names it, with that slice's
secrets, so its owner decides which slices it serves:

- **Exposure (`daemon_slice_exposures`).** A daemon's owner exposes it to chosen
  slices (`gs agent expose <daemon> <slice>` or the account page) and withdraws
  with `unexpose`. The owner must be able to read the slice.
- **Choosing.** `SetSliceCIDaemon` requires the caller to own or administer the
  slice's account and either manage the daemon or have it exposed to the slice.
  Choosing a managed daemon automatically exposes it to that slice.
- **Withdrawing.** Dispatch re-checks exposure on every new patchset, rerun, and
  reconnect; a withdrawn daemon never receives secrets and required checks fail
  until re-exposed or reassigned.

## Conversations linked to patchsets

Every patchset records the agent conversation that produced it and the
conversation event `seq` at creation time, so the changeset/patchset UI can show
the exact exchange behind each revision.

- **Model:** `patchsets.authoring_conversation_id` +
  `patchsets.authoring_conversation_seq` (migration `0015`). Patchset N's
  exchange is the events with `prev_cutoff < seq <= authoring_conversation_seq`,
  where `prev_cutoff` is the previous patchset's seq for the *same* conversation.
- **Write path:** the agent workspace is stamped with the conversation id during
  in-process `HostCallbacks.WorkspaceInit` (`WorkspaceConfig.ConversationID`).
  `UpdateChangesetRequest.conversation_id` forwards it; the server validates the
  conversation belongs to the changeset's slice and records the link with the
  conversation's current `LatestEventSeq` as the cutoff.
- **Read path:** `AgentService.GetConversationEvents(conversation_id, after_seq,
  before_seq)` returns the bounded slice. CLI: `gs cs conversation [changeset]
  [--patchset N]`. Web: an "Agent conversation" panel on the changeset detail
  page shows the messages behind the selected patchset.
- **Auto-capture:** after each turn, the daemon invokes
  `HostCallbacks.CaptureChangeset` in-process (the equivalent of `gs cs capture`).
  It snapshots workspace edits, no-ops when there are none, creates the changeset
  on first use (or adds a patchset), and echoes the captured patchset back into
  the conversation as a status event.

## Conversation lifecycle (close / resume)

A conversation has a durable lifecycle **status** and an orthogonal live
**reachability**:

- **Status (`agent_conversations.status`): `active` | `inactive`.** The DB is the
  source of truth. A conversation is born `active` and only becomes `inactive`
  when explicitly closed. `inactive` is terminal for the workspace: the on-disk
  dir is deleted (the Postgres transcript is kept). Runtime failures are surfaced
  as `error` events, leaving the conversation `active`.
- **Reachability (`Conversation.daemon_online`):** a read-time annotation set
  from the live hub, true when the daemon currently holds a `Connect` stream.

Do **not** infer status from what the agent currently has on disk. Continuity
comes from the server replaying `StartConversation` for every `active`
conversation on (re)connect (`replayDaemonConversations`).

### Close / delete flow

1. `CloseConversation(conversation_id)` persists `status = "inactive"` immediately.
2. If the daemon is online, the hub pushes `CloseWorkspace{conversation_id, delete_workspace}`.
   The daemon cancels any in-flight turn, closes the `codex app-server` session,
   revokes the conversation's child key, drops the conversation from memory, and
   removes (or archives to `conversations/.archived/<id>/`) the workspace.
3. If the daemon is offline, right after `StartConversation` replay on reconnect
   the server sends `ReconcileWorkspaces{active_conversation_ids}` so the daemon
   deterministically reaps any ready local workspace not in the active set.

### Re-hydration & two-history resume semantics

- If a `ready` conversation's directory vanished from disk,
  `handleStartConversation` checks `workspaceDirExists(conv.workdir)` and falls
  through to re-hydrate it automatically.
- **Two histories:** the Postgres transcript (`agent_conversation_events`) is
  the source of truth for the UI, while Codex's session context resumes via
  `codexThreadID` stored in `.agent-meta.json` (`thread/resume {threadId}`).
  Whenever `thread/resume` fails or no thread ID exists, `openThread` falls back
  to `thread/start` and lazily fetches the persisted transcript via
  `GetConversationEvents`, rendering a bounded summary into
  `developerInstructions` so conversational continuity survives fresh-dir
  restarts.

## Implementation notes

- **Shutdown cannot `GracefulStop` the gRPC server.** gRPC is multiplexed over
  the HTTP listener via `grpcServer.ServeHTTP`, whose `serverHandlerTransport`
  does not implement `Drain()`. Shutdown bounds the HTTP drain
  (`serverShutdownTimeout`) and then calls `grpcServer.Stop()`.
- Runtime token deltas (`message_delta`, `reasoning_delta`) are persisted with
  normal conversation seqs (marked `ephemeral` for client coalescing) so
  reasoning/thinking output survives reloads and patchset conversation ranges.

## Future work

- Additional runtimes (Claude Code, etc.) behind the `Runtime` interface.
- Daemon-initiated changeset submit surfaced directly in the conversation.
- Back-pressure on slow web subscribers.
