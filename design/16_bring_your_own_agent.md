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
4. Daemon `AgentEvent` → persist event (assign `seq`) → fan out to all live
   `StreamConversation` subscribers for that conversation.
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
  data_json, created_at, unique(conversation_id, seq))`

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

### Web: Agents tab (`web/src/routes/SliceDetailPage.tsx` + new components)

A new tab lists the subject's online daemons and the slice's conversations, with
a chat panel. Sends via `SendAgentMessage`; receives via `StreamConversation`
(fetch + ReadableStream); loads history from `GetConversation` /
`ListConversations`.

## Phasing

1. **Foundation (server) — DONE.** proto + generated stubs, migration,
   `AgentStore` (interface + memory + postgres), hub service, server/gateway
   wiring, rpc test with an in-test echo daemon. *(owned by main agent)*
2. **CLI daemon — IN PROGRESS.** `gs agent start/status/stop` + codex runtime.
   *(codex worktree `codex/agent-cli`)*
3. **Web Agents tab — IN PROGRESS.** chat UI with streaming. *(codex worktree
   `codex/agent-web`)*
4. **Changeset integration + polish — TODO.** agent edits flow to a changeset
   visible on the slice; e2e coverage.

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

## Future work

- Dedicated long-lived **daemon service token** decoupled from the interactive
  session token.
- Multiple runtimes (Claude Code, etc.) behind the `Runtime` interface.
- Daemon-initiated changeset submit surfaced directly in the conversation.
- Reconnect/resume hardening and back-pressure on slow web subscribers.
