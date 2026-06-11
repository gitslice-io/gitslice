# Future Work For Production

This proof of concept intentionally uses PostgreSQL plus a filesystem object
store to validate the native source graph, slice projection, submit integrity,
and local scalability characteristics. Before production, the system needs the
work below.

## Storage And Durability

- Replace the prototype filesystem object store with a durable object store
  adapter that supports multi-host writes, read-after-write consistency for
  committed objects, lifecycle policies, encryption, and checksummed transfers.
- Add object-store write leases and finalization records so blob and tree
  metadata cannot point at partially written objects.
- Implement garbage collection for staged blobs, unreachable tree nodes,
  abandoned patchsets, and projection artifacts with grace periods and
  reachability rechecks.
- Add online migration strategy, schema version gates, rollback plans, and
  production migration rehearsals for large PostgreSQL tables.
- Define backup, restore, point-in-time recovery, and disaster-recovery drills
  for both PostgreSQL metadata and object-store bytes.

## Scalability

- Add scale indexes for changed paths, slice coverage, patchset read/write
  sets, submit requirements, and projection cache lookup.
- Add background workers for index rebuilds and prove they are fully
  reconstructable from source-of-truth commits, patchsets, and slice
  definitions.
- Move projection refresh to a measured worker pool with queue depth, latency,
  retry, and invalidation metrics.
- Add sharding or partitioning plans for hot tables such as patchsets,
  path_heads, pending_publish, sessions, and operational indexes.
- Define read-replica behavior for large query workloads while preserving
  linearizable writes for refs and submit finalization.
- Keep load tests as release gates with explicit throughput, p50, p95, p99,
  conflict-rate, retry-rate, and projection-latency budgets.

## Integrity And Correctness

- Run the storage integrity verifier continuously as an admin job and expose
  summarized findings through operational dashboards.
- Add repair workflows for missing object bytes, corrupt tree nodes, stale
  path_heads, stuck pending publishes, and inconsistent projection caches.
- Add chaos and fault-injection tests for object-store write failures,
  PostgreSQL failover, publisher restarts, duplicate retries, and partial
  network failures.
- Add end-to-end invariants that compare native refs, projected Git refs,
  path_heads, and content-addressed tree traversal after high-contention load.
- Add signed audit records for submit decisions, path-base validation, ref CAS
  movement, and administrative slice-definition changes.

## Security And Multi-Tenancy

- Replace fake development auth with production identity integration, token
  rotation, service-account management, scoped credentials, and session
  revocation.
- Enforce authorization in every service method using account membership,
  repository visibility, slice roles, and path-level policy.
- Add tenant isolation tests that prove users cannot infer unauthorized account,
  slice, path, changeset, or blob existence.
- Add encryption at rest, transport security, secret management, audit logging,
  and least-privilege database roles.
- Add rate limits and quota controls for uploads, changesets, projection
  requests, Git compatibility endpoints, and API clients.

## Product Completeness

- Implement hydration, dehydration, workspace operation logs (`gs op log`),
  undo, diff, and conflict-resolution CLI workflows.
- Add changeset list/show/abandon/explain commands and stable JSON schemas for
  all CLI commands.
- Implement review, approvals, required checks, submit requirement provenance,
  and CI/build-test integration.
- Implement Git push-to-changeset translation while preserving the same native
  submit validation used by `gs`.
- Add slice-definition editing workflows, administrative review for policy
  weakening, and overlap-aware projection explanations.

## Operations

- Add structured metrics, tracing, pprof controls, logs with request IDs,
  dead-letter queues, and operational runbooks.
- Add deployment manifests, health/readiness checks, graceful draining,
  zero-downtime rollouts, and capacity planning.
- Add SLOs for auth, status, submit acceptance, publish latency, projection
  freshness, Git clone/fetch latency, and integrity verification.
- Add cost and storage accounting for object bytes, projection caches,
  PostgreSQL growth, and retained changeset history.
