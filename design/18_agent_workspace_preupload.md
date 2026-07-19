# 18. Agent Workspace Pre-Upload And Fast Capture

## Status

Proposed.

Implementation note (2026-07-15): the first rollout item is implemented. Final
capture now reports phase timings and counts in verbose diagnostics, hashes
workspace files with a bounded worker pool, batches blob-status requests, and
uploads missing immutable cache objects through the existing bounded concurrent
upload executor. Agent-daemon capture enables the diagnostics automatically and
writes them to daemon stderr without adding them to the conversation. The stat
index, watcher, staged pre-upload protocol, and background transfer remain
unimplemented.

## Goal

Make end-of-turn agent capture fast and predictable by preparing immutable blob
objects while an agent is still working, without weakening changeset
correctness, changing the native storage model, or making filesystem watchers a
source of truth.

The agent daemon will:

- monitor each active conversation workspace for file changes;
- debounce and hash stable file versions into the existing client object cache;
- pre-upload eligible missing blobs in bounded background batches;
- keep a recoverable workspace index mapping paths to observed content;
- perform an authoritative reconciliation when the turn ends; and
- create or update exactly one conversation-linked patchset only after the
  authoritative capture and its bundled checks are complete.

Pre-upload is preparation, not publication. It must never create a patchset,
move a ref, submit a changeset, or make an intermediate workspace snapshot the
repository source of truth.

## Motivation

Today a successful agent turn finishes in this order:

```text
agent final message
  -> scan and hash the workspace
  -> ask which blobs are available
  -> upload each missing blob
  -> run applicable in-slice checks
  -> CreateChangeset or UpdateChangeset
  -> emit captured-patchset status
  -> emit turn_complete
```

`turn_complete` intentionally follows capture, so the web UI continues to show
the agent as working after the final prose response. That is correct as a
lifecycle rule, but it becomes confusing when capture is slow.

The motivating production turn changed 139 paths as part of a Go module-path
migration. The final agent message was persisted at `05:54:56.783936Z`; the
patchset was created at `06:00:06.768456Z`; and the captured status plus
`turn_complete` arrived at `06:00:17Z`. The workspace and patchset ultimately
matched exactly, but finalization took about five minutes and twenty seconds.

Two current implementation details amplify large captures:

1. `scanWorkspaceFiles` walks and hashes every materialized workspace file,
   copying bytes through the client object cache even when the file is
   unchanged.
2. `attachBlobIDs` batches the availability lookup but uploads missing content
   hashes sequentially. The CLI already has a bounded concurrent upload worker
   pool for other commands, but capture does not use it.

Background preparation lets hashing and network transfer overlap the useful
work already happening during the turn. The final capture becomes a short
reconciliation and metadata transaction rather than `add + push + CI` performed
only after the final response.

There are three distinct optimizations and they must not be conflated:

1. Concurrent final hashing and upload shortens today's critical path without
   adding a long-lived watcher.
2. Pre-upload moves network latency before the turn boundary. Merely putting
   bytes in the local object cache does not currently shorten
   `scanWorkspaceFiles`, because `cache.PutFile` reads and hashes the source
   before discovering that the object already exists.
3. Reusing a previously computed path hash avoids final file reads, but requires
   a separate, conservative stat-fingerprint cache in the authoritative scan.
   This is the only optimization here that can silently capture stale content
   if implemented incorrectly.

The rollout therefore starts with concurrency and measurement, treats the stat
shortcut as its own guarded feature, and adds continuous watching only when the
measured remaining latency justifies its lifecycle and privacy cost.

## Non-goals

- Do not use Git as the internal source of truth or create a hidden Git
  repository in an agent workspace.
- Do not create rolling or intermediate patchsets for every filesystem event.
- Do not treat watcher events, stat metadata, or a local index as proof of final
  workspace content.
- Do not skip server-side blob verification, changeset validation, bundled
  checks, optimistic patchset concurrency, or submit requirements.
- Do not upload paths outside the conversation's single bound slice.
- Do not expose global content existence across accounts or slices.
- Do not require the web browser or central server to reach the daemon directly.
- Do not make successful background preparation a prerequisite for capture.

If the watcher, index, cache, staging lease, or network is unavailable, normal
end-of-turn capture must still succeed through its authoritative fallback path.

## Design principles and invariants

### Native objects remain authoritative

Raw file content is identified using the existing Gitslice content-hash and
blob-id rules. PostgreSQL changeset and patchset metadata remains authoritative;
object storage contains immutable payloads; Git remains a compatibility layer.

### Pre-upload does not publish

A pre-uploaded blob is not a workspace snapshot, patchset, commit, or ref. It
becomes part of visible source history only when `UpdateChangeset` validates and
records file edits that reference it.

### Final capture is authoritative

The daemon must reconcile the workspace at the end of every successful turn.
It may reuse indexed hashes and uploaded blob ids only after validating that the
corresponding file identity still matches the final workspace observation.

### Patchset creation stays atomic

The server must see one complete `UpdateChangesetRequest` with the final edit
set, expected current patchset, conversation linkage, and bundled check results.
No partial patchset is visible while blobs are being prepared.

### Content races fail safe

If a file changes while it is being hashed, checked, or finalized, the daemon
must discard the stale observation and retry. It must never associate a path
with a blob produced from a different file generation.

### Background work is bounded and optional

Every queue, worker pool, retry loop, and progress stream has a bound. A busy
conversation cannot starve other conversations on the daemon. Disabling
pre-upload changes performance only, not behavior.

### Authorization does not expand

The daemon uses its existing authenticated subject and the conversation's bound
slice. The server rechecks slice write authorization for staging and again when
the patchset is written. Knowing a global content hash must not reveal whether
another account already stores it.

### Checks describe captured content

Bundled check results must correspond to the same workspace manifest recorded
in the patchset. Pre-upload never runs or reports checks by itself.

## Current and proposed flows

### Current

```text
runtime turn
  -> final message
  -> full workspace scan and hash
  -> batch GetBlobStatus
  -> serial missing-blob uploads
  -> checks
  -> patchset
  -> turn_complete
```

### Proposed

```text
runtime turn
  | filesystem events
  v
watcher -> dirty coalescer -> stable hasher -> local CAS
                                      |
                                      v
                         status batcher -> upload scheduler
                                              |
                                              v
                                    staged remote blobs

runtime completes
  -> capture barrier
  -> authoritative reconciliation
  -> drain/reuse matching uploads
  -> upload only final misses
  -> run checks for the final manifest
  -> atomically create/update patchset
  -> captured status
  -> turn_complete
```

The background path is speculative. The final path is deterministic.

## Components

### Conversation preloader

Each ready `agentConversation` owns one `workspacePreloader`. It starts after
workspace hydration succeeds and stops when the conversation is closed, its
workspace is reaped, or the daemon shuts down.

Suggested internal shape:

```go
type workspacePreloader interface {
    Start(context.Context) error
    Finalize(context.Context) (*WorkspaceManifest, error)
    Stop()
    Stats() PreloadStats
}
```

The concrete implementation coordinates:

- a recursive filesystem watcher;
- a dirty-path generation map and debounce timers;
- a persistent workspace index;
- the existing immutable client object cache;
- daemon-wide hash and upload schedulers;
- periodic authoritative reconciliation; and
- an optional server-side pre-upload session.

The preloader does not call `CreateChangeset`, `UpdateChangeset`, submit, sync,
or any ref mutation.

### Daemon-wide scheduler

Worker pools are daemon-wide, not multiplied without limit per conversation.
The scheduler provides:

- a CPU/hash semaphore;
- a network upload semaphore;
- per-conversation fairness;
- latest-generation queue coalescing;
- byte and item limits; and
- retry timing with jitter.

Initial defaults:

```text
hash workers globally:       min(max(2, NumCPU/2), 8)
upload workers globally:     min(max(4, NumCPU), 16)
uploads per conversation:    4
debounce quiet period:       750 ms
availability batch window:   250 ms
availability batch size:     512 hashes
reconciliation interval:     30 s with jitter
progress event interval:     at most every 5 s
```

These are implementation defaults, not protocol guarantees. The daemon may
later expose advanced tuning, but v1 should avoid a large public flag surface.

### Workspace manifest

A manifest is the daemon's immutable description of one observed workspace
state:

```text
manifest_version
base_commit_id
slice_definition_hash
entries sorted by canonical global path:
  path
  operation       # upsert or delete relative to the base snapshot
  content_hash    # upsert only
  blob_id         # when known
  mode
manifest_hash     # domain-separated hash of the canonical entries
```

The manifest is not a server object and does not replace tree construction. It
is an internal capture artifact used to prove that pre-uploaded content, checks,
and the final request refer to the same observed files.

## Workspace index

### Purpose

The existing client object cache stores immutable bytes by content hash but
does not remember which workspace path and filesystem observation produced a
hash. The new index supplies that path-to-object mapping.

Store it at `.gs/preupload_index.json` initially. If write amplification or file
size becomes material, replace JSON with a small embedded store without changing
the logical schema.

### Schema

```text
version
workspace_identity:
  slice_id
  definition_hash
  base_commit_id
entries[path]:
  file_identity:
    size
    mode
    mtime_ns
    ctime_ns when available
    device and inode when available
  generation
  content_hash
  blob_id
  remote_state       # unknown | staged | available
  observed_at
  uploaded_at
last_reconciled_at
last_captured_manifest_hash
last_captured_patchset_id
```

No token, secret, file content, or server credential is stored in the index.
Content remains in the existing client object cache.

### Persistence

- Write with a temporary file, `fsync` where supported, and atomic rename.
- Use mode `0600`.
- Flush after a short interval, after a bounded number of mutations, at capture,
  and during graceful shutdown.
- Treat a malformed, missing, or newer-version index as a cache miss and rebuild
  it. Never fail workspace hydration solely because the index is unusable.
- Discard content mappings when `slice_id`, `definition_hash`, or base semantics
  are incompatible. Cached immutable objects may remain globally reusable.

### Stat cache correctness

Filesystem metadata is an optimization, not a content identity. An entry may
reuse a cached hash when its file identity is unchanged and the cached object
still exists. Conservative cases must rehash, including:

- timestamps equal to or newer than the index flush time (the racy-timestamp
  case familiar from Git indexes);
- missing inode or ctime support;
- filesystems with coarse timestamps;
- a watcher overflow or reconciliation uncertainty;
- mode or size changes; and
- any entry explicitly marked dirty.

The authoritative final scan still walks the workspace, validates the complete
path set, and computes the edit set against the base snapshot. The stat shortcut
only avoids rereading file bytes. It must initially be off by default and
independently selectable from background pre-upload. A size-plus-mtime shortcut
is explicitly forbidden: `cp -p`, `rsync -a`, checkout tools, editor atomic
replacement, and coarse timestamp resolution can preserve that pair while
changing content. When the strong fingerprint is incomplete or doubtful, the
only safe answer is a content rehash.

An optional filesystem-monitor token can reduce future scans, but it never
eliminates end-of-turn reconciliation.

## Watcher and reconciliation

### Watch implementation

Use a small watcher abstraction backed by `fsnotify` on supported platforms.
Register every materialized directory under the workspace root except paths
excluded by the shared workspace traversal policy. Add watches when directories
are created and remove them when directories disappear.

Normalize events into path dirtiness rather than interpreting platform-specific
event sequences as final operations:

```text
create/write/chmod -> mark path dirty
remove             -> mark deletion candidate
rename             -> mark old path and parent dirty; discover new path by scan
directory change   -> reconcile that subtree
overflow/error     -> mark workspace uncertain and schedule full reconciliation
```

Atomic-save patterns commonly appear as create-temp, write, chmod, and rename.
Coalescing by final canonical path plus reconciliation handles them without
depending on a particular editor sequence.

### Shared eligibility and ignore policy

Watcher traversal and final capture must share one implementation for:

- workspace-root containment;
- slice included-path membership;
- canonical path validation;
- `.git`, `.gs`, `.gitslice`, and agent-metadata exclusions;
- supported file types and modes; and
- any future Gitslice-native ignore file.

Duplicating `shouldSkip` rules in the watcher would eventually make pre-upload
and capture disagree.

Speculative remote upload may be stricter than final capture, but never broader.
In addition to the shared capture exclusions, it supports a configurable
secret-path denylist for files such as local environment files, credentials,
and private keys. A denylisted file is simply deferred to authoritative final
capture if it remains an eligible final edit; the watcher must not treat the
denylist as a source exclusion. This reduces, but cannot eliminate, the risk of
uploading transient sensitive content in `all` mode.

Recursive-watch registration is also bounded. If platform watch-descriptor
limits are approached, the preloader reports the condition and falls back to
periodic reconciliation rather than partially watching a tree without saying
so.

### Debounce

Each path carries a monotonic in-memory generation. New activity increments the
generation and resets its quiet timer. Only the latest generation is eligible
for hashing. Queue entries contain `(path, generation)`; workers discard stale
entries before doing expensive work and again before publishing their result.

Debounce is not a correctness boundary. A file can change immediately after its
quiet period; stable-read verification and final reconciliation handle that.

### Periodic reconciliation

Watch APIs can drop events, overflow, fail on network filesystems, or miss paths
created before recursive registration. Therefore the daemon periodically walks
the workspace using the same traversal policy as capture.

Reconciliation compares the observed path set and stat identities with the
index, marks differences dirty, records deletion candidates, and clears the
uncertain flag only after a complete successful walk.

If watching cannot be established, the preloader degrades to reconciliation-only
mode. This costs more I/O but preserves the optimization and correctness model.

## Stable hashing

For each `(path, generation)`:

1. Open the path without following it outside the workspace.
2. `fstat` the opened file and validate its supported type, mode, and path.
3. Stream it through the existing content hasher into the client object cache.
4. `fstat` the same descriptor again.
5. Compare the pre/post identity and size.
6. Under the path lock, verify that the queued generation is still current.
7. Record the content hash only if all validations match; otherwise mark the
   path dirty again.

The object written from a stale generation is harmless immutable cache content.
It must not replace the current path entry.

For large files, hashing is streaming and subject to the existing blob-size
limits. Deletes and pure mode changes require no content upload.

## Remote pre-upload

### Eligibility modes

Background transfer changes privacy timing: it can upload a transient file that
is deleted before final capture. Final capture today would never transfer that
file. The product must make that distinction explicit.

Support three modes:

```text
off    no background remote transfer; watcher/index may still hash locally
known  pre-upload paths already present in the base snapshot or current patchset
all    pre-upload every final-capture-eligible stable path, including new files
```

Rollout begins with `off`, then makes `known` the default after validation.
`all` remains an explicit daemon choice until the UI/CLI clearly discloses that
new transient files may be transferred before capture. Final capture always
uploads every final eligible file regardless of mode.

Longer debounce for new paths is useful but is not an adequate privacy policy by
itself.

### Staged pre-upload session

The target design uses a server-side staged pre-upload session scoped to:

```text
subject_id + daemon_id + conversation_id + slice_id
```

Uploading under this session:

- requires write authorization on the bound slice;
- verifies content hash and size using existing blob rules;
- stores or reuses immutable object bytes idempotently;
- creates a short-lived reachability lease;
- does not create a patchset or ref;
- does not make the blob readable through ordinary slice blob reads; and
- does not disclose whether another account already has the same global hash.

Normal `blob_slices` visibility is established only when a validated patchset
references the blob. The server may physically deduplicate bytes globally, but
status responses are scoped to hashes already accessible to the slice or staged
by this pre-upload session.

This is stricter than directly calling today's `UploadBlob`, which immediately
associates uploaded content with the slice. A prototype may use the existing
RPC behind an explicit `all` opt-in, but default background transfer should ship
with staged visibility.

### Protocol surface

One possible minimal API is:

```proto
message BeginBlobPreuploadRequest {
  string conversation_id = 1;
}

message BeginBlobPreuploadResponse {
  string session_id = 1;
  google.protobuf.Timestamp expires_at = 2;
}

message GetPreuploadBlobStatusRequest {
  string session_id = 1;
  repeated string content_hashes = 2;
}

message UploadBlobHeader {
  // existing fields...
  string preupload_session_id = N;
}

message UpdateChangesetRequest {
  // existing fields...
  string preupload_session_id = N;
}
```

The server derives subject, slice, daemon, and conversation ownership from the
session; clients cannot substitute them. `UpdateChangeset` validates that the
session belongs to the request's conversation and authoring slice, promotes the
referenced blobs, and may release the consumed leases after the transaction.

Unary small-blob and streaming large-blob upload remain supported. A later
multi-blob client stream can reduce framing and round trips, but it is not
required to overlap transfer with the agent turn.

### Availability batching

The preloader groups stable hashes for a short window and queries at most 512 per
status request, matching the existing bulk lookup pattern. Duplicate hashes
across paths or conversations collapse before scheduling.

The scheduler uploads missing hashes concurrently. Completion is recorded for a
path only if its generation and content hash remain current. A stale uploaded
version remains leased/orphaned and is eventually collected.

### Retry and offline behavior

- Retry transient RPC and object-store failures with exponential backoff and
  full jitter.
- Do not retry authorization, invalid hash, unsupported size, or invalid-path
  failures until relevant state changes.
- Pause background network work when the daemon's server connection is known
  offline, while continuing bounded local hashing.
- Prioritize final-capture misses over speculative background work.
- Surface repeated failures in daemon diagnostics and metrics, not as a failed
  agent turn until authoritative capture also fails.

## Final capture protocol

`workspacePreloader.Finalize` replaces repeated ad hoc scanning with a defined
barrier. It runs after the runtime reports success and while the conversation's
existing `runMu` prevents another daemon turn from executing.

### Steps

1. Emit a durable phase event such as `finalizing changes: reconciling
   workspace`; the web UI switches from `Agent is working` to `Capturing
   changes`.
2. Record a pending-finalization journal entry containing the user event seq,
   the last durable daemon `client_seq` from the completed runtime turn, and the
   start time.
3. Increment the capture epoch and run an authoritative workspace
   reconciliation.
4. For every final path, reuse an indexed object only when its final stat
   identity and cache entry are valid; otherwise perform stable hashing.
5. Build the canonical manifest and compare it with the last captured manifest.
   If they match and the current changeset/patchset is still live, return the
   no-change result without checks or an update RPC.
6. Drain in-flight uploads whose `(hash, session)` matches the manifest. Cancel
   or deprioritize stale speculative work.
7. Query status for every required manifest hash in one or more bounded batches.
8. Upload remaining misses concurrently through the finalization priority lane.
9. Run applicable in-slice checks against the final manifest.
10. Revalidate the workspace observation. If it changed while checks ran,
    discard those check results and retry reconciliation/checks a bounded number
    of times. If the workspace remains continuously active, fail capture with a
    clear `workspace changed during finalization` error rather than recording
    mismatched results.
11. Call `CreateChangeset` when needed, then `UpdateChangeset` with the complete
    edit set, expected current patchset, pre-upload session, bundled check runs,
    and exact conversation cutoff.
12. Persist the captured manifest and patchset id in the local index.
13. Emit the captured-patchset status followed by durable `turn_complete`.
14. Clear the pending-finalization journal.

Creating a changeset before checks remains consistent with current behavior,
but no patchset is created until all required blobs and bundled results are
ready. If `CreateChangeset` succeeded and later finalization fails, the empty
draft is recoverable and reusable; no source revision was published.

### Exact conversation cutoff

The current server records `patchsets.authoring_conversation_seq` using the
conversation's latest event at `UpdateChangeset` time. A user message arriving
during slow finalization can therefore be included in the preceding patchset's
conversation range even though it did not produce that patchset.

Use the daemon's existing durable `client_seq` to make the causal boundary
explicit:

```proto
message UpdateChangesetRequest {
  // existing fields...
  int64 authoring_conversation_client_seq = N;
}
```

Before the update, the daemon waits until that client seq is acknowledged as
persisted. The server resolves `(conversation_id, client_seq)` to its canonical
conversation `seq` and stores that value as the patchset cutoff. Later user
messages and capture-status events are excluded. Legacy clients that omit the
field retain current latest-event behavior.

### Check consistency

The first implementation may continue running checks in the conversation
workspace, followed by manifest revalidation. The stronger long-term model is
to materialize an immutable local check directory from the manifest and client
object cache, so checks cannot observe concurrent external edits. This is an
independent improvement and should not block background pre-upload.

Pause speculative hashing and upload while in-place bundled checks run. Keep
coalescing filesystem dirtiness, then reconcile after the checks. Shared ignore
rules should exclude ordinary build output, but pausing also prevents a check
that writes many files from creating an I/O and upload feedback loop. Final
manifest revalidation remains required because a check may modify an eligible
source file.

Check result caching is also separate. A result may be reused only with a key
covering at least the manifest hash, resolved check definition, environment or
image identity, and runner version.

## Turn and daemon lifecycle

### Pending-finalization journal

The final agent message is visible before capture ends. If the daemon is stopped
in that window, the server can retain a user-visible final response without the
captured status or `turn_complete` marker.

Persist pending finalization under `.gs` before the capture barrier. On daemon
restart and conversation replay:

- validate that the conversation is still active and the workspace identity
  matches;
- finish or safely retry capture before accepting another user turn;
- resend any unacknowledged durable events using existing client-seq semantics;
  and
- clear the journal only after `turn_complete` is acknowledged or the
  conversation is explicitly closed.

The journal contains metadata only, not file content or credentials.

### Graceful daemon stop

Stopping the daemon cancels speculative watcher, hash, and upload work promptly.
It flushes the index and pending-finalization journal but does not create a
patchset merely because the process is stopping. A later restart resumes an
active conversation's pending finalization. Explicit conversation close remains
terminal and deletes or archives the workspace according to the BYOA lifecycle.

### Server reconnect

Background local work survives a transient `Connect` reconnect because it is
rooted in the daemon base context, like a turn. Network pre-upload pauses or
retries independently. Capture and its terminal events continue to use the
existing ack/resend buffer.

### Rehydration and sync

Hydration creates a new index after base materialization. Sync, rebase, conflict
resolution, or slice-definition changes invalidate affected entries and schedule
reconciliation. The watcher must not interpret daemon-authored hydration writes
as user edits; it starts after hydration or runs under a bulk-update suppression
epoch followed by one reconciliation.

## Orphan objects, leases, and garbage collection

Speculative upload necessarily produces objects that may never be referenced:
an intermediate generated file, a stale generation, an abandoned turn, or a
closed conversation.

The storage design already permits blobs to arrive before metadata publication
and requires reachability-based GC. Pre-upload sessions add short-lived staged
blob leases as roots. Recommended behavior:

- lease TTL comfortably exceeds expected offline retry and capture windows;
- active preloaders refresh leases in bounded batches;
- patchset creation promotes referenced blobs to ordinary live reachability;
- closing a conversation releases its leases best-effort;
- expiry is sufficient when a daemon disappears;
- GC quarantines and rechecks expired unreachable blobs before deletion; and
- final capture treats a GC'd staged object as a cache miss and reuploads it.

Correctness never depends on a speculative object surviving: the local object
cache or final workspace can recreate it. Leases reduce churn and cost.

Metrics must distinguish staged bytes, promoted bytes, duplicate/deduplicated
bytes, expired bytes, and orphan age. Account-level quotas should include staged
bytes so background agents cannot bypass storage limits.

## Security and privacy

### Data-transfer disclosure

Users must know that `all` mode may transfer a stable intermediate file even if
it is deleted before capture. `known` mode reduces that expansion by limiting
remote preparation to paths already represented in source history. Local
hashing alone does not transfer data and may remain enabled in `off` mode.

### Scope enforcement

- Canonicalize every event path under the workspace root.
- Reject symlink or rename escapes using the same path primitives as capture.
- Check inclusion in the single bound slice before hashing metadata becomes an
  upload candidate.
- Reauthorize the pre-upload session and final changeset operation server-side.
- Never accept a client-supplied account or slice that conflicts with the
  session's stored ownership.

### Blob visibility

Staged blobs are not ordinary slice-readable blobs. `GetBlobStatus` and read
operations keep their existing authorization semantics. Pre-upload status is
session-scoped and must not become a global content-existence oracle.

### Local state

Index and journal files use `0600`, live under excluded `.gs` state, and contain
no bytes or credentials. The immutable client object cache already contains
workspace content and retains its existing local security requirements.

## User experience

The web UI must distinguish model/runtime work from capture work:

```text
Agent is working
Capturing changes: reconciling workspace
Capturing changes: 124/139 blobs ready
Running 2 checks
Captured changeset 42 patchset 3
```

Only phase transitions are durable conversation events. Fine-grained counters
may be live/ephemeral and throttled; they should not add hundreds of transcript
rows. Reload reconstructs the active phase from the last durable transition and
terminal event.

If the daemon goes offline with a pending finalization, show `Agent offline
while capturing changes` rather than indefinite `Agent is thinking`. On restart,
show `Resuming capture`.

CLI diagnostics should report pre-upload mode, watcher state, last
reconciliation, queue depths, staged hit ratio, and last error under `gs agent
status --verbose` or debug output.

## Observability

### Metrics

At minimum:

```text
agent_preupload_events_total{kind}
agent_preupload_dirty_paths{daemon,conversation}
agent_preupload_hash_duration_seconds
agent_preupload_hashed_bytes_total
agent_preupload_status_batches_total
agent_preupload_upload_duration_seconds
agent_preupload_uploaded_bytes_total{outcome}
agent_preupload_queue_depth{kind}
agent_preupload_retries_total{reason}
agent_preupload_reconcile_duration_seconds
agent_preupload_reconcile_misses_total
agent_preupload_watch_descriptors
agent_preupload_orphan_bytes_total
agent_capture_duration_seconds{phase}
agent_capture_required_blobs
agent_capture_preuploaded_blobs
agent_capture_fallback_uploads
agent_capture_manifest_retries_total
agent_capture_recovery_total{outcome}
```

Avoid raw conversation ids and paths as unbounded metric labels. Put those in
structured debug logs with appropriate redaction.

### Structured timings

Every capture should log one summary containing:

- changed path count;
- unique blob count and bytes;
- index hits and rehashes;
- staged hits and final misses;
- scan, hash, status, upload, checks, update, and event-ack durations;
- manifest retry count; and
- final result.

This instrumentation ships before enabling remote pre-upload broadly so rollout
decisions use measured phase data.

## Performance objectives

For a workspace quiet for at least one reconciliation interval, with a healthy
server and no long-running checks:

- at least 95% of final required blob bytes should already be staged;
- final missing-blob preparation should complete in under 5 seconds at p95 for
  ordinary source changes;
- no-change turns should finalize in under 2 seconds at p95;
- watcher/index idle CPU should remain negligible;
- queues and memory remain bounded under generated-file storms; and
- disabling pre-upload should preserve current correctness and provide a
  measurable fallback baseline.

These objectives exclude the execution time of repository-defined checks, which
is reported separately.

## Failure matrix

| Failure | Background behavior | Final capture behavior |
|---|---|---|
| Watcher unavailable | periodic reconciliation only | authoritative scan |
| Watcher overflow | mark uncertain, full reconcile | wait for/repeat reconcile |
| File changes during hash | discard stale generation | stable rehash |
| File changes during checks | no special action | discard results and retry |
| Local index corrupt | rebuild | full scan/hash |
| Client cache entry missing | rehash path | rehash/upload |
| Server offline | retain bounded local state | retry/fail clearly; no patchset |
| Auth expired | stop remote work | normal auth error; no patchset |
| Upload interrupted | retry idempotently | status-check then retry |
| Lease expired/GC ran | mark remote state unknown | reupload from cache/workspace |
| Daemon stopped mid-capture | persist journal | resume on restart |
| Conversation closed | cancel and reap | no capture; discard/release lease |
| Patchset concurrency conflict | keep prepared blobs | refresh/retry through existing rules |
| Continuous external writes | coalesce generations | bounded retries then explicit error |

## Testing strategy

### Unit tests

- Event normalization for create, write, chmod, rename, remove, directory
  creation, atomic replacement, overflow, and watcher error.
- Fake-clock debounce and latest-generation queue collapse.
- Stable hashing with writes before open, during read, after read, and after
  result publication.
- Index load, atomic persistence, version mismatch, corruption, racy timestamp,
  base change, and missing cache object.
- Eligibility modes and shared ignore/path rules.
- Availability batching, hash deduplication, bounded upload concurrency,
  fairness, cancellation, retry classification, and backoff.
- Manifest determinism and no-change fast path.
- Pending-finalization recovery and client-seq cutoff mapping.
- Staged-session ownership, authorization, expiry, promotion, and non-visibility
  through ordinary reads.

Use injected watcher, filesystem, clock, hasher, and blob-client interfaces;
tests must not rely on platform timing sleeps.

### Race and stress tests

- Run the Go race detector over watcher/index/finalize state transitions.
- Generate thousands of rapid rewrites across multiple conversations and assert
  bounded worker and queue counts.
- Rewrite one path continuously while capture runs and verify no stale hash is
  recorded.
- Stop/restart the daemon at every finalization phase boundary.
- Simulate reconnect and ack loss while capture status and `turn_complete` are
  buffered.

### Service tests

- Pre-upload cannot access another subject's conversation or slice.
- Global object deduplication does not create a cross-slice status oracle.
- A patchset promotes only blobs it references and only from a compatible
  session.
- Expired unreferenced staged blobs are reported unreachable; live patchset
  blobs remain roots.
- `UpdateChangeset` resolves the explicit daemon client seq to the correct
  canonical conversation cutoff.

### End-to-end tests

Use a real server and PostgreSQL for:

- a large multi-file agent turn whose uploads overlap runtime work;
- a final capture with zero fallback uploads;
- a new-file task under `known` and `all` modes;
- daemon stop and restart during finalization;
- agent/server reconnect during background upload;
- concurrent conversations sharing daemon-wide limits;
- checks bundled against the exact captured manifest; and
- GC/lease expiry followed by successful final fallback upload.

Record phase timings and assert semantic outcomes rather than fragile wall-clock
speed, except for opt-in performance/load tests.

## Rollout plan

### Phase 0: measure and fix the current critical path

- Add capture phase timings and counts.
- Reuse the existing bounded concurrent uploader in `attachBlobIDs`.
- Parallelize capture hashing behind bounded, daemon-wide I/O limits where
  measurements show that hashing is material.
- Add explicit `Capturing changes` UI state before optimization.

This is low risk, helps human CLI capture as well as agents, and should
substantially improve large captures immediately. Measure again before
authorizing the watcher phases; if this meets the latency objective, continuous
pre-upload can remain optional.

### Phase 1: guarded authoritative-scan index

- Extract shared traversal/ignore/path policy.
- Add the rebuildable path/stat/content index without requiring a watcher.
- Keep its rehash shortcut off by default, require a strong fingerprint, and
  retain a forced-full-hash mode as the correctness oracle.
- Add property and fuzz coverage proving that indexed and full-hash captures
  produce byte-identical edit sets under adversarial write interleavings.
- Validate correctness and quantify scan/hash savings before enabling it.

### Phase 2: watcher dark mode and staged protocol

- Add stable hashing, watcher, debounce, periodic reconciliation, bounded
  scheduling, secret-path denial, and descriptor-limit fallback.
- Keep remote pre-upload off while measuring event loss, queue pressure, idle
  cost, and how often background hashes still match final content.
- Add server-side staged pre-upload sessions, scoped status, leases, quotas, and
  non-visibility through ordinary slice reads.

### Phase 3: known-path pre-upload

- Enable background upload for base/current-patchset-known paths behind a daemon
  feature flag.
- Add daemon-wide scheduling, retry, progress, metrics, and quotas.
- Roll out `known` as the default after observing staged-hit ratio, orphan rate,
  and capture fallback behavior.

### Phase 4: finalization recovery and exact linkage

- Persist the pending-finalization journal.
- Resume capture after daemon restart.
- Add the explicit conversation client-seq cutoff to `UpdateChangeset`.
- Show offline/resuming capture states in the web UI.

These changes can ship earlier if lifecycle correctness takes priority over
background transfer.

### Phase 5: all-path opt-in and transport optimization

- Expose and document `all` mode for users who accept transient new-file
  transfer.
- Consider a multi-blob streaming upload protocol and transport compression if
  per-object framing remains material after background overlap.
- Consider immutable local check materialization and safe check-result caching.

Each phase is independently useful and can be disabled without invalidating
existing workspaces or patchsets.

## Alternatives considered

### Only parallelize end-of-turn uploads

Do this first. It is simple and likely turns the motivating five-minute serial
upload into tens of seconds. It does not eliminate full workspace hashing,
network work after the final response, or the need for accurate progress and
recovery.

### Periodic full scans without a watcher

This is a valid fallback and may be sufficient for small workspaces. Repeatedly
hashing the whole slice wastes I/O and performs poorly for large materialized
trees. A stat index plus watcher reduces steady-state cost while periodic scans
retain correctness.

### Upload immediately on every filesystem event

Rejected. Editors and generators emit partial writes and event storms. Immediate
upload wastes bandwidth, increases transient-data exposure, and amplifies stale
generation races. Debounce, stable hashing, batching, and backpressure are
required.

### Make the watcher authoritative

Rejected. Watchers can miss events, overflow, and have platform-specific rename
semantics. Final reconciliation remains mandatory.

### Create a patchset periodically during the turn

Rejected. This creates noisy revisions, exposes incomplete work, runs checks on
unstable states, complicates conversation cutoffs, and changes the user-visible
meaning of a patchset. Only immutable blobs are prepared early.

### Use Git's index and pack protocol internally

Rejected as an internal source model. Gitslice can borrow the useful mechanics
of a stat index, local content-addressed cache, negotiation, batching, and
compression without making Git repositories or commits authoritative.

### Unbounded per-conversation workers

Rejected. A daemon may host many conversations; multiplicative worker pools can
exhaust CPU, file descriptors, memory, bandwidth, and server quotas. Scheduling
is daemon-wide with per-conversation fairness.

### Skip final blob status checks because pre-upload reported success

Rejected. Leases can expire, GC can run, acknowledgements can be lost, and
stale generations can finish. Final status verification is cheap and protects
patchset integrity.

### Treat background upload errors as turn failures

Rejected. Pre-upload is optional. Only failure of the authoritative final
capture should prevent `turn_complete`, and that failure must be surfaced
clearly.

## Relationship to Git

Git separates object preparation from publication:

1. `git add` hashes changed files, writes local blob objects, and records path to
   object-id mappings in the index.
2. `git commit` writes small tree and commit objects locally.
3. `git push` negotiates missing reachable objects, packs them into a compressed
   stream, transfers them, and updates the remote ref only after receipt.

Gitslice capture currently performs the conceptual equivalents of add, push,
checks, and patchset creation at the end of the turn. The proposed watcher/index
does the local `add`-like preparation incrementally; staged pre-upload overlaps
the transfer; and `UpdateChangeset` remains the sole atomic publication step.

The analogy stops there. Gitslice patchsets, trees, PostgreSQL metadata, slice
authorization, and submit validation remain native and authoritative.

## Independent architecture review

The design was reviewed independently with the local Claude CLI in read-only
plan mode after it inspected the storage, API, agent, CI, cache, and capture
code. Its verdict was that content addressing makes pre-upload a sound cache
warmer as long as watcher state never becomes authoritative.

The review changed the design in four material ways:

- it separates pre-hash, pre-upload, and final rehash avoidance, because only
  the latter two reduce different parts of finalization;
- it puts concurrent final hashing/uploads and measurement before a watcher;
- it makes the stat shortcut separately guarded and requires byte-equivalence
  tests against a forced full capture; and
- it adds explicit controls for transient-secret transfer, check-generated
  write storms, watch-descriptor limits, and orphan-volume measurement.

The review recommended using the existing blob RPCs with no new API. This
design intentionally does not adopt that point for default background transfer:
the current `UploadBlob` implementation associates a blob with the slice
immediately, while the required privacy boundary says speculative content is
not ordinarily slice-readable until a patchset references it. Existing RPCs
remain suitable for an explicit prototype; staged sessions are required before
`known` becomes the default.

## Decisions

- Proceed with background workspace preparation as an optimization.
- Keep final capture authoritative and patchset creation atomic.
- Ship concurrent final uploads and phase instrumentation first.
- Measure before committing to a watcher; retain periodic scanning as the
  simpler alternative when it is sufficient.
- Treat stat/content reuse as a guarded optimization separate from pre-upload.
- Use a rebuildable persistent index plus watcher and periodic reconciliation
  only after the earlier phases justify it.
- Use daemon-wide bounded scheduling with per-conversation fairness.
- Introduce staged pre-upload sessions before enabling background transfer by
  default.
- Default remote eligibility to known paths initially; require explicit opt-in
  for all paths because of transient-file transfer.
- Preserve a normal full-capture fallback for every failure mode.
- Add pending-finalization recovery and an exact daemon-client-seq conversation
  cutoff.
- Do not adopt Git as internal storage or create intermediate patchsets.

## Open questions

1. Should `known` remain the permanent default, or can `all` become default
   after stronger ignore controls and product disclosure ship?
2. Should staged upload leases use a dedicated table or the general
   `reachability_roots` mechanism described in the storage design?
3. What TTL and account quota policy best balance offline recovery with orphan
   cost?
4. Is one multi-blob bidirectional stream materially better than bounded
   concurrent existing uploads after background overlap is enabled?
5. Should check execution move to an immutable local manifest checkout in the
   same project, or remain a separate correctness improvement?
6. Which capture phase transitions should be durable transcript events versus
   ephemeral live progress?
7. Should local hashing remain enabled when remote mode is `off`, or should a
   single flag disable all monitoring for users who prefer zero idle activity?
