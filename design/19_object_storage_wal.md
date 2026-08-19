# Object-Storage WAL For Write Throughput

Status: DRAFT — not scheduled. Phase 0 measurement gate must pass before any
implementation phase begins.

This document plans an evolution of the publish path: a per-target-ref
write-ahead log (WAL) in object storage becomes the durability and
linearization point for accepted commits, with PostgreSQL demoted to a derived
(but still transactional and queryable) apply target for that log.

The idea is borrowed from Cursor's "Git at any scale" Continuity architecture
(https://cursor.com/blog/git-at-any-scale): external object storage holds an
authoritative WAL; local state is a warm cache; pushes ack only after the WAL
persists; replicas verify freshness with conditional GETs. We keep our native
object model — nothing here reintroduces Git internals — but we adopt the same
substrate-level move: take the single-primary relational database out of the
accepted-history commit point.

Related docs:

- [01_gitslice_architecture_design.md](01_gitslice_architecture_design.md) §12
  (submit flow, target-ref landing sequencer)
- [02_storage.md](02_storage.md) §4 (Postgres as source of truth), §8
  (replication — the section this document effectively rewrites)
- [07_conflict_resolution.md](07_conflict_resolution.md) (path-head CAS
  admission)

## 1. Goals And Non-Goals

Goals:

- Raise sustained landings/sec on a hot target ref well beyond what a
  single-primary Postgres commit transaction allows.
- Remove object-store round-trips from inside lock-holding Postgres
  transactions.
- Make accepted history durable and replayable independently of Postgres: a
  lost or corrupted metadata database becomes a re-apply job, not data loss.
- Open a path to multi-region read freshness checks (conditional GET against
  the WAL head) and eventually regional publishers.

Non-goals:

- Changing the changeset/patchset write model, admission semantics
  (path-head CAS), or any user-visible API.
- Multi-writer per target ref (the landing sequencer stays; WAL CAS is the
  correctness backstop, not a coordination replacement).
- Replacing Postgres for control-plane metadata (accounts, slices, changesets,
  reviews, checks). Only the accepted-publish log and ref-head authority move.
- Git-native packfiles or any Git-format storage. The WAL stores native
  commit-chain records.

## 2. Current Write Path And Bottleneck Analysis

As implemented in `internal/postgres/changeset_store.go` (`PublishPending`):

```text
1. Admission (SubmitChangeset): PG txn compares patchset path predicates
   against path_heads, advances path_heads, appends pending_publish(sequence).
2. Publisher: ONE PG transaction that:
   a. SELECT ... FOR UPDATE SKIP LOCKED over pending_publish
   b. loads the ref head
   c. per patchset, sequentially: treestore.ApplyEdits — object-store
      (R2) reads/writes for tree nodes — INSIDE the open transaction
   d. computes the native commit chain
   e. refreshes path_heads rows
   f. inserts commit rows, outbox rows, marks pending/changesets
   g. locks the refs row, verifies CAS, moves the ref
   h. COMMIT — this is the commit point for accepted history
```

Consequences:

- The commit point is a Postgres transaction whose duration includes O(edits)
  R2 round-trips. R2 latency (~tens of ms per op, see 2026-06-16 perf notes)
  dominates; row locks on changesets and eventually `refs` are held throughout.
- Throughput per target ref ≈ 1 / (batch tree-build time + txn overhead), and
  batches serialize behind each other on the primary.
- Every landing must reach the US-region primary; there is no story for
  regional write coordination beyond "make Postgres bigger."
- Durability of accepted history is exactly Postgres durability. Tree/blob
  bytes are already in R2, but the *ordering* — which commits are accepted, in
  what order, on which ref — lives only in Postgres.

Two distinct fixes fall out, and only the second needs the WAL:

- **Fix A (no WAL): get R2 out of the transaction.** Build the tree chain and
  commit objects *before* opening the PG txn; the txn shrinks to pure metadata
  writes + ref CAS. Content-addressed tree writes are idempotent, so orphaned
  nodes from a failed publish are harmless GC candidates
  ([02_storage.md](02_storage.md) §5 already states this rule).
- **Fix B (WAL): move the commit point itself off Postgres.** After Fix A the
  ceiling becomes the serialized short PG txn on the primary; the WAL replaces
  that with an object-storage conditional append, and PG apply becomes a
  derived, asynchronous, idempotent step.

## 3. Proposed Architecture

### 3.1 WAL Layout

Per target ref, in the production object store (R2):

```text
wal/{target_ref_key}/seg/{seq:020d}          # one record per publish batch
wal/{target_ref_key}/snapshot/{seq:020d}     # periodic state snapshot
```

`target_ref_key` is a stable encoding of the ref name (e.g. hex of its hash
plus a readable suffix). `seq` is a dense, monotonically increasing batch
sequence starting at 1. Segment keys are immutable once written.

### 3.2 WAL Record

A protobuf message (`gitslice.wal.v1.PublishRecord`), one per publish batch:

```text
PublishRecord:
  seq
  target_ref
  prev_record_hash          # chain hash of record seq-1 ("" for seq 1)
  base_commit_id            # ref head this batch was built on
  commits[]                 # full canonical commit objects, in chain order
                            #   (id, parent_ids, root_tree_id, author,
                            #    message, created_at, changed_paths)
  included[]                # (changeset_id, patchset_id, commit_id) triples
  outbox_events[]           # the same events the publisher writes today
  writer                    # publisher identity + lease token
  written_at
```

Rules:

- Every `root_tree_id` and required blob referenced by `commits[]` must be
  durable in the object store **before** the record is appended. (Same
  precondition as today's rule that metadata never references an unverified
  blob — the commit point moves, the precondition doesn't.)
- `commits[0].parent_ids[0] == base_commit_id`, and `base_commit_id` must equal
  the last commit of record `seq-1`. Replay validates both, plus
  `prev_record_hash`, so a broken chain is detected, never silently applied.
- The record embeds everything needed to rebuild the Postgres side: commit
  rows, `commit_changed_paths`, outbox events, changeset submitted-state.

### 3.3 Append Protocol (Linearization)

The publisher already holds the target-ref sequencer lease. The WAL append is
the correctness backstop for lease bugs and split-brain, exactly mirroring the
role ref CAS plays today ([01](01_gitslice_architecture_design.md) §12.4):

```text
1. Publisher knows head seq N (cached; re-listable from the WAL prefix).
2. Build the batch entirely outside any PG transaction (Fix A).
3. PUT wal/.../seg/{N+1} with a create-only conditional write
   (If-None-Match: *). Exactly one writer can win seq N+1.
4. Success => the batch is durably accepted. This is the new commit point.
5. Failure (precondition failed) => another writer owns the seq; re-read the
   head record, rebase/rebuild, retry. Equivalent to today's ErrConflict on
   ref CAS.
```

R2 supports conditional writes (`If-None-Match`/etag preconditions); Phase 0
verifies semantics and measures p50/p99 conditional-PUT latency before
anything else is built.

### 3.4 Postgres Becomes The Applier

A new applier consumes the WAL in seq order and applies each record
idempotently in one (now object-store-free, therefore short) PG transaction:

```text
per record:
  insert commits + commit_parents + commit_changed_paths (on conflict do nothing)
  refresh path_heads post-publish rows
  mark pending_publish rows published
  mark changesets submitted; refresh stack statuses
  insert outbox events (idempotency_key = target_ref/seq/event position)
  advance refs row to the record's last commit, guarded by an applied-seq
  watermark (wal_applied(target_ref, last_seq)) instead of value CAS
```

In the fast path the publisher itself runs the apply synchronously right after
a successful append, so end-to-end submit latency does not regress. The
decoupled applier exists for crash recovery (append succeeded, apply didn't)
and, later, as the standing consumer that lets appends run ahead of Postgres.

Read semantics:

- Normal reads (web, CLI status, projections) keep reading Postgres — now a
  bounded-lag derived view during any applier backlog.
- Freshness-sensitive reads (admission base checks, Git fetch head
  negotiation, "is my submit visible") consult the WAL head with a conditional
  GET when the applied watermark is behind, mirroring Cursor's ETag check.
  In the synchronous fast path the watermark is current and this costs nothing.

### 3.5 Snapshots, Truncation, GC

Every K records (or T minutes) the publisher writes
`snapshot/{seq}`: ref head commit id, applied watermark, chain hash. Segments
older than the last two snapshots minus a retention window become GC-eligible
through the existing reachability framework — add a
`REACHABILITY_ROOT_KIND_WAL_SNAPSHOT` root so the mark/quarantine/sweep flow
([02](02_storage.md) §5.1) governs WAL deletion like everything else. Retention
must exceed applier-lag alarms, replica lag, and the disaster-replay window we
want to keep (suggest: 30 days of segments minimum; snapshots forever — they
are tiny).

### 3.6 What Stays Exactly As Is

- Admission: path-head CAS in Postgres remains the same-path conflict
  authority at submit time. It is not on the publish critical path and is not
  the throughput bottleneck.
- The target-ref landing sequencer/lease.
- Changeset/patchset/review/checks metadata: Postgres source of truth.
- Blob staging/verification, tree construction, projection caches.

## 4. Invariant Changes

[02_storage.md](02_storage.md) §9 and [01](01_gitslice_architecture_design.md)
§18 must be amended when (and only when) Phase 3 flips authority:

- "PostgreSQL is the metadata source of truth" narrows to control-plane
  metadata. For accepted publish history and target-ref heads, the WAL is the
  source of truth and Postgres is a rebuildable derived view (joining the
  existing invariant 22 family: derived indexes are rebuildable from
  source-of-truth objects — the WAL becomes one of those source objects).
- "A ref update is atomic and conditional" is satisfied by the conditional
  segment create; the `refs` row becomes the applied view of the WAL head.
- New invariant: a WAL record is immutable once written; record seq N+1's
  base must equal record N's final commit; replay of the full WAL from any
  snapshot reproduces identical commit ids and ref heads.

## 5. Phased Plan

Each phase gates the next. Abort criteria are explicit because the honest
possibility is that Fix A alone is sufficient for years.

### Phase 0 — Measure And Verify (gate)

- Extend `tests/load` with a sustained-landing benchmark against a
  staging-shaped environment (real R2 + real Postgres/Neon): landings/sec and
  p50/p95 submit-to-visible latency at batch sizes 1/8/64.
- Microbenchmark R2 conditional PUT (If-None-Match) p50/p99, both single-key
  contention and clean-key append, from the Cloud Run region.
- Verify R2 conditional-write semantics under concurrent writers (two racing
  create-only PUTs to the same key: exactly one winner, loser gets a
  precondition failure).
- Output: a dated entry in `design/10_execution_log.md` with numbers and an
  agreed target (e.g. "sustain X landings/sec on refs/global/main with p95
  submit-to-visible < Y s").
- **Abort/hold criterion:** if measured demand headroom over current capacity
  is >10x, stop after Phase 1 and file the rest as future work.

### Phase 1 — Fix A: Shrink The Publish Transaction (no WAL)

Restructure `PublishPending`:

1. Read pending batch + ref head in a short read txn (or plain reads with the
   lease held).
2. Build tree chains and commit objects with **no open PG transaction**,
   parallelizing tree-node writes where the treestore allows.
3. One short PG txn: re-verify ref CAS, insert everything, move ref.
   On CAS failure the built trees are orphaned content-addressed nodes —
   harmless, GC-eligible, exactly like a failed `PublishCommit` today.

- No schema changes, no new infra, no semantic change. This phase is worth
  shipping regardless of the WAL's fate.
- Exit: Phase 0 benchmark rerun; expect the largest single jump in
  landings/sec of the whole plan. Log before/after.

### Phase 2 — WAL Shadow Write (dual-write, PG authoritative)

- Define `proto/wal/v1/publish_record.proto`; add a WAL writer in
  `internal/walstore` (thin layer over the object store client).
- Behind `GITSLICE_PUBLISH_WAL=shadow`: after the PG publish txn commits, the
  publisher appends the corresponding record. WAL failure logs and increments
  a metric but does not fail the publish.
- Build `gs admin wal verify` (or a maintenance binary): replays the WAL chain
  and diffs against Postgres — commit ids, order, ref head, changed paths.
- Run continuously on staging; nightly parity check in CI or cron.
- Exit: N weeks of 100% parity on staging + production shadow, including
  through deploys and publisher restarts.

### Phase 3 — WAL-First Commit Point (authoritative flip)

- Behind `GITSLICE_PUBLISH_WAL=authoritative`: append the WAL record **before**
  the PG apply; the append is the commit point. The publisher then applies to
  PG synchronously (fast path unchanged in latency terms).
- Add the recovery applier: on startup / lease acquisition, compare WAL head
  seq to the applied watermark and re-apply any gap before publishing new
  batches. All apply writes become idempotent (`on conflict` guards, watermark
  table `wal_applied(target_ref, last_seq, applied_at)` in a new migration).
- Ref-row update switches from value CAS to watermark-guarded apply.
- Crash-injection tests (kill between append and apply; kill mid-apply;
  concurrent publisher with expired lease racing the conditional PUT) join the
  e2e suite.
- Rollback story: flip the flag back to `shadow`; PG was applied for every
  acked submit, so reverting authority loses nothing.
- Exit: full e2e + load suites green under crash injection; staging soak;
  production flip.

### Phase 4 — Decoupled Apply And Freshness Reads (throughput unlock)

- Let the publisher append batch N+1 while the applier is still applying N
  (bounded pipeline depth). Submit acks on append durability.
- Freshness-sensitive read paths gain the WAL-head conditional-GET check when
  the watermark lags; outbox consumers may tail the WAL directly instead of
  the outbox table (records already carry the events).
- Applier-lag SLO + alerting; lag counts against the freshness bound
  advertised to admission checks.
- Exit: Phase 0 target met or exceeded; lag SLO holds under load test.

### Phase 5 (future, unscheduled) — Regional Leverage

- Regional read nodes serve from object storage + local derived state,
  verifying freshness against the WAL head (Cursor's replica model, our
  objects).
- Snapshot/truncate GC fully automated via reachability roots.
- Only then: evaluate per-ref regional publishers.

## 6. Failure Modes

```text
Append wins, apply crashes        recovery applier replays the gap from the
                                  watermark; idempotent apply
Two publishers (lease expiry)     conditional create arbitrates; loser rebuilds
                                  on the new head — same shape as today's
                                  ErrConflict
Applier lags                      PG reads are stale within a bound; freshness-
                                  sensitive paths check the WAL head; alerting
                                  on lag SLO
WAL chain gap/corruption          replay validates seq density, base linkage,
                                  prev_record_hash; publisher refuses to append
                                  past a broken chain (loud failure, not silent)
R2 unavailable                    publishes fail closed (submits stay pending),
                                  identical blast radius to R2 loss today, which
                                  already blocks tree writes
Postgres lost                     restore control-plane from backup, then replay
                                  WAL from last snapshot — accepted history is
                                  no longer hostage to PG backups
```

## 7. Open Questions

1. Exact R2 conditional-write guarantees under concurrent same-key creates
   (Phase 0 must prove exactly-one-winner; if unproven, fall back to a
   PG-arbitrated seq allocation with WAL as durability-only until proven).
2. Per-append latency cost in the fast path (one extra R2 RTT before ack) —
   acceptable if batching amortizes it; Phase 0 measures.
3. Whether outbox consumers move to WAL-tailing in Phase 4 or stay on the
   table indefinitely (both are correct; table is simpler, tail removes writes
   from the apply txn).
4. Encryption/tenancy: WAL records contain commit metadata (authors, messages,
   paths) — confirm the bucket's controls match Postgres-level expectations
   before production shadow writes (cf. security review 2026-07-11).
5. Snapshot cadence and whether snapshots should also capture path_heads
   digests to accelerate disaster replay.

## 8. What We Are Explicitly Not Copying From Cursor

- Git packfiles/local Git repos as the compute engine — our native model stays.
- UDP gossip replication — irrelevant until Phase 5 regional reads exist, and
  even then conditional GETs may suffice at our scale.
- Elastic per-repo replica fleets — slices are projections over one graph; our
  read scaling remains indexes + caches + (later) regional derived state.
