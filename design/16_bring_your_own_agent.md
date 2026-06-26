# 16. Bring Your Own Agent (BYOA)

## Goal

Let a user run their own coding agent on their own machine and drive it from the
Gitslice web UI. The user runs `gs agent start` in an empty directory; that
process becomes a long-lived **agent daemon**. From the **Agents** tab of a slice
detail page the user can open one or more **conversations** with that daemon.
Each conversation runs the agent (initially the `codex` CLI) inside its own
`gs` workspace sub-directory bound to the slice. Conversation traffic is streamed
between the browser and the daemon, relayed by the central Gitslice server.

## Why a relay

The daemon runs on the user's laptop, behind NAT, with no inbound connectivity.
It therefore cannot be a server the browser dials. Instead the daemon holds a
single persistent **outbound bidirectional gRPC stream** to the central server,
and the server acts as a **hub** that routes messages between browser and daemon,
keyed by conversation id.

```
Browser ── unary SendAgentMessage ─▶ Server ── ServerMessage (bidi) ─▶ Daemon ─▶ codex
Browser ◀── StreamConversation (SSE) ─ Server ◀── DaemonMessage (bidi) ─ Daemon ◀─ codex
```

All conversations for one daemon multiplex over that daemon's single `Connect`
stream, tagged by `conversation_id`.

## Decisions (v1)

- **Daemon scope: account-wide.** A daemon belongs to the subject that started
  it. Each *conversation* binds to exactly one slice (preserving the
  one-workspace-one-slice rule). The same daemon can serve conversations for any
  slice the subject can write.
- **Web receive transport: server-streaming RPC via the gRPC-gateway**
  (`StreamConversation`), consumed in the browser with `fetch` + `ReadableStream`,
  matching the existing `ReadBlobStream` / `ImportGitRepositoryStream` pattern.
  Web send is a unary RPC.
- **Daemon auth: the saved `gs` user token** (standard `authorization: Bearer`
  metadata), same as every other CLI command. A dedicated long-lived daemon
  token is future work (see below).
- **Agent runtime: `codex`** behind a small `Runtime` interface so other
  runtimes can be added later.
- **Durability: every conversation event is persisted** to Postgres with a
  per-conversation monotonic `seq`. History reloads from the DB; a reconnecting
  web stream replays from `after_seq` then tails live.

## Components

### Proto: `AgentService` (`proto/core/v1/agent.proto`)

Daemon side (long-lived bidi):

- `rpc Connect(stream DaemonMessage) returns (stream ServerMessage)`

  First `DaemonMessage` must be `RegisterDaemon{name, runtime, version}`; server
  replies `DaemonRegistered{daemon_id}`. Thereafter the daemon sends `Heartbeat`,
  `AgentEvent` (streamed codex output for a conversation), and
  `ConversationStarted` (workspace ready). The server sends `StartConversation`,
  `DeliverUserMessage`, `CancelConversation`, and `Ping`.

Web side (unary + one server-stream, all surfaced through the gateway):

- `rpc ListDaemons(ListDaemonsRequest) returns (ListDaemonsResponse)`
- `rpc CreateConversation(CreateConversationRequest) returns (Conversation)`
- `rpc ListConversations(ListConversationsRequest) returns (ListConversationsResponse)`
- `rpc GetConversation(GetConversationRequest) returns (Conversation)`
- `rpc SendAgentMessage(SendAgentMessageRequest) returns (SendAgentMessageResponse)`
- `rpc StreamConversation(StreamConversationRequest) returns (stream ConversationEvent)`

### Server hub (`service/agent.go`, `service/agent_hub.go`)

In-memory registry:

- `daemon_id → daemonConn` (a send channel to the daemon's `Connect` stream).
- `conversation_id → set of subscriber channels` (live web `StreamConversation`
  calls).

Flow:

1. Daemon `Connect` → authenticate subject → upsert `agent_daemons` row
   (status `online`) → register `daemonConn` in the hub. On stream end, mark
   `offline` and drop the entry.
2. Web `CreateConversation{daemon_id, slice}` → authz (subject owns daemon &
   can write slice) → insert `agent_conversations` row → if daemon online, push
   `StartConversation` over its stream.
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

### Data model (`internal/postgres/migrations/0014_agents.sql`)

- `agent_daemons(id pk, subject_id, account, name, runtime, version, status,
  last_seen_at, created_at)`
- `agent_conversations(id pk, daemon_id fk, subject_id, slice_id, account,
  slice_name, title, status, workspace_subdir, next_seq, created_at,
  updated_at)`
- `agent_conversation_events(id pk, conversation_id fk, seq, role, type, text,
  data_json, item_id, created_at, unique(conversation_id, seq))`

### CLI: `gs agent` (`internal/cli/agent.go`)

- `gs agent start [--name N] [--runtime codex]` — must be run in an empty dir;
  authenticate with saved creds, open `Connect`, register, then service
  `StartConversation` / `DeliverUserMessage` commands. Each conversation gets a
  sub-directory `./conversations/<conversation_id>/`, hydrated as a `gs`
  workspace bound to the conversation's slice, where `codex exec` runs. Codex
  stdout/events are streamed back as `AgentEvent`s.
- `gs agent status` / `gs agent stop` — local management.

A `Runtime` interface abstracts the agent process; `codexRuntime` is the first
implementation (wrapping `codex exec`, reusing the patterns in CLAUDE.md).

### Web: Agents page (`web/src/routes/SliceAgentsPage.tsx` + components)

Agents is its own per-slice route, `/slices/$account/$slice/agents`, a peer of
Changesets and Settings in the slice header (not a sub-tab of the file browser).
It lists the subject's online daemons and the slice's conversations, with a chat
panel. Sends via `SendAgentMessage`; receives via `StreamConversation` (fetch +
ReadableStream); loads history from `GetConversation` / `ListConversations`. The
reusable chat UI lives in `web/src/components/slices/AgentsTab.tsx`.

## Phasing

1. **Foundation (server) — DONE.** proto + generated stubs, migration,
   `AgentStore` (interface + memory + postgres), hub service, server/gateway
   wiring, rpc test with an in-test echo daemon.
2. **CLI daemon — DONE.** `gs agent start/status/stop` + codex runtime.
3. **Web Agents tab — DONE.** chat UI with streaming.
4. **Changeset integration + polish — DONE.** Conversation↔patchset linkage
   (below) plus deterministic per-turn auto-capture: after each agent turn the
   daemon runs `gs cs capture`, which snapshots the workspace edits and records
   them as a conversation-linked patchset (creating the changeset on first use,
   adding a patchset thereafter, and skipping turns with no edits).

## Conversations linked to patchsets

Every patchset records the agent conversation that produced it and the
conversation event `seq` at creation time, so the changeset/patchset UI can show
the exact exchange behind each revision.

- **Model:** `patchsets.authoring_conversation_id` +
  `patchsets.authoring_conversation_seq` (migration `0015`). Patchset N's
  exchange is the events with `prev_cutoff < seq <= authoring_conversation_seq`,
  where `prev_cutoff` is the previous patchset's seq for the *same* conversation.
- **Write path:** the agent workspace is stamped with the conversation id at
  hydration (`gs workspace init --agent-conversation <id>`, written to
  `WorkspaceConfig.ConversationID`). `gs cs update` forwards it as
  `UpdateChangesetRequest.conversation_id`; the server validates the conversation
  belongs to the changeset's slice and records the link with the conversation's
  current `LatestEventSeq` as the cutoff (computed server-side, so the CLI never
  tracks seqs).
- **Read path:** `AgentService.GetConversationEvents(conversation_id, after_seq,
  before_seq)` returns the bounded slice. CLI: `gs cs conversation [changeset]
  [--patchset N]`. Web: an "Agent conversation" panel on the changeset detail
  page shows the messages behind the selected patchset.
- **Auto-capture:** the daemon calls the hidden `gs cs capture` after each turn.
  It snapshots the workspace edits, no-ops when there are none, creates the
  changeset on first use (or adds a patchset), and forwards
  `WorkspaceConfig.ConversationID` so the patchset is linked. A status event
  echoes the captured patchset back into the conversation.

## Conversation lifecycle (close / resume)

A conversation has a durable lifecycle **status** and an orthogonal live
**reachability**. Keeping them separate is the whole point of this section.

- **Status (`agent_conversations.status`): `active` | `inactive`.** The DB is the
  source of truth. A conversation is born `active` and only becomes `inactive`
  when the user explicitly closes it. `inactive` is terminal for the workspace:
  the on-disk dir is deleted (the Postgres transcript is kept). We collapse the
  older `closed` value into `inactive`, and `error` is **not** a status — runtime
  failures are surfaced as `error` *events*, leaving the conversation `active`.
- **Reachability (`Conversation.daemon_online`): a read-time annotation**, set by
  the server from the live hub, true when the conversation's daemon currently
  holds a `Connect` stream. This drives "show but disable the composer when the
  agent is offline". It is **not** persisted and **not** derived from status: an
  `active` conversation whose daemon is offline is a normal, valid state.

Do **not** infer status from what the agent currently has on disk. The agent
routinely starts in a **fresh, empty workspace dir**; continuity comes from the
server replaying `StartConversation` for every `active` conversation on
(re)connect (`replayDaemonConversations`), i.e. the DB re-hydrates the agent —
never the reverse. Deriving `active` from disk presence would flip every
conversation to `inactive` on any fresh-dir restart or daemon-offline window.

### Close / delete flow

1. **Web `CloseConversation(conversation_id)`** (new unary RPC) → authz → `Agents.
   SetConversationStatus(id, "inactive")`. Persisted immediately, regardless of
   whether the daemon is reachable.
2. **If the daemon is online**, the hub pushes a server→daemon
   `CloseWorkspace{conversation_id, delete_workspace}` `ServerMessage`. The daemon
   handler `cancelRun()`s any in-flight turn, `closeSession()`s the codex process,
   drops the conversation from its in-memory map, and removes the workspace:
   `os.RemoveAll(conv.workdir)` when `delete_workspace` (default), or moves it to
   `conversations/.archived/<id>/` when archiving.
3. **If the daemon is offline**, the push is lost; the dir is reaped **lazily and
   deterministically** via reconciliation. Right after the `StartConversation`
   replay on (re)register, the server sends one `ReconcileWorkspaces{active_
   conversation_ids}` carrying the full active set for that daemon
   (`replayDaemonConversations`). The daemon (`handleReconcileWorkspaces`) removes
   the workspace of any conversation it holds locally that is **not** in the set.
   It only reaps *ready* conversations, so a conversation mid-hydration — which is
   either an active (re)start already in the set, or in progress — is left alone.
   This needs no grace window: the active set is an explicit snapshot, not a
   timing guess.

### Re-hydration when the workspace is missing

When the server sends `StartConversation` for an `active` conversation whose dir
is gone, `handleStartConversation` self-heals **only if the daemon has no ready
in-memory state** for it: `createConversation` yields a fresh, not-ready conv and
`hydrateWorkspace` rebuilds the dir via `gs workspace init`. But if the daemon
already holds the conv as `ready` (e.g. the dir was deleted out from under a
running daemon), the `isReady()` short-circuit **skips hydration** and codex is
later launched with `cmd.Dir` pointing at a missing directory, which fails with
no auto-repair. **Fixed:** `handleStartConversation` now short-circuits only when
the conv is ready in memory *and* `workspaceDirExists(conv.workdir)`; a ready conv
whose dir vanished falls through to `hydrateWorkspace` and is rebuilt.

### Resume semantics — two histories, only one is preserved for the agent

There are two distinct "histories", and resume must not conflate them:

1. **The user-visible transcript** — `agent_conversation_events` in Postgres.
   Source of truth for the web UI (`StreamConversation` replay). Always survives;
   **never fed to codex.**
2. **Codex's own session context** — resumed *only* via `codexThreadID`
   (persisted in the workspace's `.agent-meta.json`). `OpenSession` →
   `openThread` issues `thread/resume {threadId}`.

`openThread` **silently falls back to `thread/start` — a fresh thread with zero
history — whenever resume fails**: the id aged out, `.agent-meta.json` was lost
(deleted/archived dir, fresh-dir restart), or codex's machine-local thread store
is gone. The workspace *files* are rebuilt by re-hydration, but the agent's
conversational memory is not, producing a silent divergence: the user sees the
full transcript while the agent starts cold.

**Fixed:** on a cold `thread/start` fallback (id absent or resume failed),
`openThread` seeds the new thread from the persisted transcript. The daemon
fetches the conversation's `agent_conversation_events` (`fetchConversationTranscript`
→ `GetConversationEvents`), renders user/agent messages + tool calls into a
bounded block (`renderTranscript`), and the runtime appends it to
`developerInstructions` for the fresh thread only. The fetch happens lazily on the
cold path — a successful `thread/resume` pays no cost — and any fetch error is
non-fatal (start cold rather than fail the turn). Resume continuity now rides on
the durable Postgres log, not on whether `.agent-meta.json` survived.

## Implementation notes

- **Shutdown can't `GracefulStop` the gRPC server.** gRPC is multiplexed over
  the same HTTP listener via `grpcServer.ServeHTTP` (`server/server.go`,
  `NewCombinedGRPCGatewayHandler`). That `serverHandlerTransport` does not
  implement `Drain()`, so `grpcServer.GracefulStop()` panics whenever a server
  stream is still open at shutdown — and a daemon's `Connect` stream always is.
  Shutdown therefore bounds the HTTP drain (`serverShutdownTimeout`) and then
  calls `grpcServer.Stop()`; the combined HTTP server has already drained
  in-flight requests gracefully by that point. See the 2026-06-21 execution-log
  entry.
- The hub holds no durable state. Live `publish` to web subscribers is
  best-effort/non-blocking; correctness comes from the persisted event log plus
  `after_seq` replay on (re)subscribe.
- Runtime token deltas (`message_delta`, `reasoning_delta`) are part of that
  persisted log. The daemon still marks them as `ephemeral` to signal coalescing
  behavior to clients, but the server assigns a normal conversation seq so
  reasoning/thinking output survives reloads and patchset conversation ranges.

## Future work

- Dedicated long-lived **daemon service token** decoupled from the interactive
  session token.
- Multiple runtimes (Claude Code, etc.) behind the `Runtime` interface.
- Daemon-initiated changeset submit surfaced directly in the conversation.
- Back-pressure on slow web subscribers.
