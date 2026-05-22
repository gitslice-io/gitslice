# Gitslice Storage Design

This document defines the native storage model for Gitslice. Product context is
in [00_product.md](00_product.md), the top-level architecture is in
[01_gitslice_architecture_design.md](01_gitslice_architecture_design.md), the
gRPC API surface is in [03_core_api.md](03_core_api.md), and derived indexes are
covered in [06_indexing.md](06_indexing.md).

## 1. Storage Goals

Gitslice storage is built around:

- Content-addressed immutable blobs
- Immutable tree nodes
- Immutable commits
- Transactional metadata
- Atomic refs
- Separate blob and metadata stores
- Deterministic canonical paths and tree hashes

The core invariant is:

```text
Ref -> Commit -> RootTree -> TreeEntries -> Blobs
```

Everything except refs is immutable.

## 2. Commit Model

Commits are immutable storage-level snapshots of the global tree.

```text
Commit:
  id
  parent_ids
  root_tree_id
  author
  message
  created_at
  changed_paths[]
  metadata
```

There is one global commit graph. All slices project views from that same graph.

This gives the system:

- Unified history
- Consistent global indexing
- Cross-slice visibility without cross-slice submission
- Deterministic slice projection

## 3. Ref Model

A ref is a mutable named pointer to a commit.

In Git terms, branches and tags are refs. In Gitslice, commits and trees are
immutable, but refs move as work is submitted.

```text
refs/global/main -> G123
```

When a changeset lands, Gitslice creates a new commit and atomically moves the
target ref:

```text
refs/global/main: G123 -> G124
```

Refs are needed because immutable commits alone do not say which commit is the
current accepted state.

### 3.1 Target Refs

A target ref is the ref a queue updates when a changeset lands.

The initial system may use one accepted global tree ref:

```text
refs/global/main
```

This must not imply one global submit queue. Many account queues can target the
same ref. They serialize only work assigned to those queues, and the final ref
update is serialized by the target-ref landing sequencer and still protected by
CAS.

Future branch support can add target refs such as:

```text
refs/global/branches/{branch}
refs/accounts/{account}/branches/{branch}
```

### 3.2 Changeset Refs

Changeset patchsets can be addressed with refs:

```text
refs/changes/{changeset_id}/{patchset_number}
```

These refs make it possible to integrate with Git tooling, CI systems, and
review systems without making changesets ordinary branches.

`refs/changes/new` can be supported as a Git push alias that asks the server to
allocate a new changeset id.

### 3.3 Projected Git Refs

When a slice is exposed as a Git repository, the Git gateway projects native
refs into Git refs.

```text
native target ref: refs/global/main
git ref:           refs/heads/main
```

Projected Git refs are compatibility views. The native source of truth remains
the global ref.

### 3.4 Atomic Ref Updates

Refs use compare-and-swap semantics.

```text
update_ref(ref, expected_old_commit, new_commit)
```

The update succeeds only if:

```text
current_commit == expected_old_commit
```

Otherwise the changeset must be rebased and retried through its required queue
or queues.

## 4. Storage Architecture

Blob content:

```text
S3-compatible object storage, GCS, R2, or equivalent
```

Metadata:

```text
transactional database or ordered key-value store
```

The metadata store must support:

```text
point lookup
range scan
transactional writes
compare-and-swap ref updates
consistent reads for submit validation
```

Implementation choices can evolve from a transactional SQL database to an
ordered distributed KV store as scale requires. The architecture depends on
capabilities, not on a specific vendor.

Search and derived indexes:

```text
metadata database tables for operational indexes
Zoekt shards for code text search
object storage for immutable index artifacts and shard snapshots
purpose-built workers for projection, policy, queue, build, and test indexes
```

Hot metadata cache:

```text
process-local cache
distributed cache where needed
```

### 4.1 High-Level Storage Interface

The storage layer exposes an internal capability interface to higher-level
services such as the changeset service, Git gateway, submit queue service, and
index workers. It is not the public product API; public clients use the gRPC API
defined in [03_core_api.md](03_core_api.md).

The interface should be narrow and source-of-truth oriented. A proto-shaped
contract:

```proto
syntax = "proto3";

package gitslice.storage.v1;

import "google/protobuf/timestamp.proto";

option go_package = "github.com/gitslice/gitslice/proto/storage/v1;storagev1";

service StorageService {
  // Source-of-truth reads.
  rpc ResolveRef(ResolveRefRequest) returns (Ref);
  rpc GetCommit(GetCommitRequest) returns (Commit);
  rpc GetTree(GetTreeRequest) returns (Tree);
  rpc ResolvePath(ResolvePathRequest) returns (ResolvePathResponse);
  rpc ListDirectory(ListDirectoryRequest) returns (ListDirectoryResponse);
  rpc ReadBlob(ReadBlobRequest) returns (stream ReadBlobResponse);
  rpc GetBlobStatus(GetBlobStatusRequest) returns (GetBlobStatusResponse);

  // Blob staging. Object storage is outside the metadata transaction.
  rpc StageBlob(stream StageBlobRequest) returns (StageBlobResponse);
  rpc VerifyBlob(VerifyBlobRequest) returns (VerifyBlobResponse);
  rpc PromoteBlob(PromoteBlobRequest) returns (PromoteBlobResponse);
  rpc AbortStagedBlob(AbortStagedBlobRequest) returns (AbortStagedBlobResponse);

  // Immutable tree construction.
  rpc ApplyTreeEdits(ApplyTreeEditsRequest) returns (ApplyTreeEditsResponse);
  rpc CreateTree(CreateTreeRequest) returns (CreateTreeResponse);
  rpc GetTreeDiff(GetTreeDiffRequest) returns (GetTreeDiffResponse);

  // Atomic commit publication and ref movement.
  rpc PublishCommit(PublishCommitRequest) returns (PublishCommitResponse);

  // Reachability and lifecycle maintenance. Restricted to storage maintenance
  // workers; normal product services should not call these directly.
  rpc ListReachabilityRoots(ListReachabilityRootsRequest) returns (ListReachabilityRootsResponse);
  rpc ListObjectReferences(ListObjectReferencesRequest) returns (ListObjectReferencesResponse);
  rpc DeleteUnreferencedObjects(DeleteUnreferencedObjectsRequest) returns (DeleteUnreferencedObjectsResponse);

  // Low-level repair/admin primitives. Normal services should prefer
  // purpose-built operations above.
  rpc ReadTransaction(ReadTransactionRequest) returns (ReadTransactionResponse);
  rpc WriteTransaction(WriteTransactionRequest) returns (WriteTransactionResponse);
}

message Ref {
  string name = 1;
  string commit_id = 2;
  google.protobuf.Timestamp updated_at = 3;
  string updated_by = 4;
}

message Commit {
  string id = 1;
  repeated string parent_ids = 2;
  string root_tree_id = 3;
  string author = 4;
  string message = 5;
  google.protobuf.Timestamp created_at = 6;
  repeated string changed_paths = 7;
  map<string, string> metadata = 8;
}

message Tree {
  string id = 1;
  string hash = 2;
  repeated TreeEntry entries = 3;
  repeated string chunk_ids = 4;
}

message TreeEntry {
  string name = 1;
  string path = 2;
  EntryKind kind = 3;
  uint32 mode = 4;
  string tree_id = 5;
  string blob_id = 6;
  string symlink_target = 7;
  int64 size = 8;
  string content_hash = 9;
}

enum EntryKind {
  ENTRY_KIND_UNSPECIFIED = 0;
  ENTRY_KIND_FILE = 1;
  ENTRY_KIND_DIRECTORY = 2;
  ENTRY_KIND_SYMLINK = 3;
}

message BlobRecord {
  string id = 1;
  string content_hash = 2;
  int64 size = 3;
  string compression = 4;
  BlobState state = 5;
  string storage_location = 6;
}

enum BlobState {
  BLOB_STATE_UNSPECIFIED = 0;
  BLOB_STATE_STAGED = 1;
  BLOB_STATE_VERIFIED = 2;
  BLOB_STATE_AVAILABLE = 3;
}

message ResolveRefRequest {
  string ref_name = 1;
}

message GetCommitRequest {
  string commit_id = 1;
}

message GetTreeRequest {
  string tree_id = 1;
}

message ResolvePathRequest {
  string commit_id = 1;
  string path = 2;
}

message ResolvePathResponse {
  TreeEntry entry = 1;
}

message ListDirectoryRequest {
  string commit_id = 1;
  string path = 2;
  string cursor = 3;
  int32 page_size = 4;
}

message ListDirectoryResponse {
  repeated TreeEntry entries = 1;
  string next_cursor = 2;
}

message ReadBlobRequest {
  string blob_id = 1;
  int64 offset = 2;
  int64 length = 3;
}

message ReadBlobResponse {
  bytes data = 1;
  int64 offset = 2;
  string content_hash = 3;
}

message GetBlobStatusRequest {
  repeated string content_hashes = 1;
}

message GetBlobStatusResponse {
  repeated BlobRecord blobs = 1;
}

message StageBlobHeader {
  string content_hash = 1;
  int64 size = 2;
  string compression = 3;
}

message StageBlobRequest {
  oneof part {
    StageBlobHeader header = 1;
    bytes data = 2;
  }
}

message StageBlobResponse {
  BlobRecord blob = 1;
}

message VerifyBlobRequest {
  string staged_blob_id = 1;
}

message VerifyBlobResponse {
  BlobRecord blob = 1;
}

message PromoteBlobRequest {
  string staged_blob_id = 1;
}

message PromoteBlobResponse {
  BlobRecord blob = 1;
}

message AbortStagedBlobRequest {
  string staged_blob_id = 1;
}

message AbortStagedBlobResponse {}

message FileEdit {
  FileEditOp op = 1;
  string path = 2;
  string old_path = 3;
  string blob_id = 4;
  string content_hash = 5;
  uint32 mode = 6;
}

enum FileEditOp {
  FILE_EDIT_OP_UNSPECIFIED = 0;
  FILE_EDIT_OP_ADD = 1;
  FILE_EDIT_OP_MODIFY = 2;
  FILE_EDIT_OP_DELETE = 3;
  FILE_EDIT_OP_RENAME = 4;
}

message ApplyTreeEditsRequest {
  string base_root_tree_id = 1;
  repeated FileEdit file_edits = 2;
}

message ApplyTreeEditsResponse {
  string root_tree_id = 1;
  repeated string changed_paths = 2;
}

message CreateTreeRequest {
  repeated TreeEntry entries = 1;
}

message CreateTreeResponse {
  string tree_id = 1;
}

message GetTreeDiffRequest {
  string old_tree_id = 1;
  string new_tree_id = 2;
  repeated string path_prefixes = 3;
}

message GetTreeDiffResponse {
  repeated string changed_paths = 1;
}

message OutboxEvent {
  string event_id = 1;
  string event_type = 2;
  bytes payload = 3;
  string idempotency_key = 4;
}

message PublishCommitRequest {
  string expected_ref_name = 1;
  string expected_old_commit_id = 2;
  repeated string parent_commit_ids = 3;
  string root_tree_id = 4;
  repeated string changed_paths = 5;
  string author = 6;
  string message = 7;
  map<string, string> metadata = 8;
  repeated string required_blob_ids = 9;
  repeated OutboxEvent outbox_events = 10;
}

message PublishCommitResponse {
  string commit_id = 1;
  Ref new_ref = 2;
}

message ReachabilityRoot {
  ReachabilityRootKind kind = 1;
  string id = 2;
  string ref_name = 3;
  string commit_id = 4;
  string changeset_id = 5;
  string patchset_id = 6;
  string cache_key = 7;
  google.protobuf.Timestamp expires_at = 8;
}

enum ReachabilityRootKind {
  REACHABILITY_ROOT_KIND_UNSPECIFIED = 0;
  REACHABILITY_ROOT_KIND_ACCEPTED_REF = 1;
  REACHABILITY_ROOT_KIND_CHANGESET_PATCHSET = 2;
  REACHABILITY_ROOT_KIND_DRAFT_WORKSPACE = 3;
  REACHABILITY_ROOT_KIND_STAGED_BLOB_LEASE = 4;
  REACHABILITY_ROOT_KIND_GIT_PROJECTION_CACHE = 5;
  REACHABILITY_ROOT_KIND_INDEX_REPAIR_CHECKPOINT = 6;
}

message ListReachabilityRootsRequest {
  string cursor = 1;
  int32 page_size = 2;
  google.protobuf.Timestamp not_after = 3;
}

message ListReachabilityRootsResponse {
  repeated ReachabilityRoot roots = 1;
  string next_cursor = 2;
}

message ObjectReference {
  string object_id = 1;
  ObjectKind kind = 2;
  repeated string referenced_object_ids = 3;
}

enum ObjectKind {
  OBJECT_KIND_UNSPECIFIED = 0;
  OBJECT_KIND_COMMIT = 1;
  OBJECT_KIND_TREE = 2;
  OBJECT_KIND_BLOB = 3;
  OBJECT_KIND_GIT_PROJECTION = 4;
  OBJECT_KIND_INDEX_CHECKPOINT = 5;
}

message ListObjectReferencesRequest {
  repeated string object_ids = 1;
  string cursor = 2;
  int32 page_size = 3;
}

message ListObjectReferencesResponse {
  repeated ObjectReference references = 1;
  string next_cursor = 2;
}

message DeleteUnreferencedObjectsRequest {
  repeated string candidate_object_ids = 1;
  string gc_generation_id = 2;
  google.protobuf.Timestamp older_than = 3;
  bool dry_run = 4;
}

message DeleteUnreferencedObjectsResponse {
  repeated string deleted_object_ids = 1;
  repeated string retained_object_ids = 2;
}

message MetadataKey {
  string space = 1;
  bytes key = 2;
}

message MetadataValue {
  MetadataKey key = 1;
  bytes value = 2;
  int64 version = 3;
}

message ReadTransactionRequest {
  repeated MetadataKey read_keys = 1;
}

message ReadTransactionResponse {
  repeated MetadataValue values = 1;
}

message MetadataCompare {
  MetadataKey key = 1;
  bytes expected_value = 2;
  bool must_not_exist = 3;
}

message MetadataMutation {
  MetadataKey key = 1;
  bytes value = 2;
  bool delete_key = 3;
}

message WriteTransactionRequest {
  repeated MetadataCompare compares = 1;
  repeated MetadataMutation mutations = 2;
  repeated OutboxEvent outbox_events = 3;
}

message WriteTransactionResponse {
  bool committed = 1;
}

enum StorageErrorReason {
  STORAGE_ERROR_REASON_UNSPECIFIED = 0;
  STORAGE_ERROR_REASON_NOT_FOUND = 1;
  STORAGE_ERROR_REASON_ALREADY_EXISTS = 2;
  STORAGE_ERROR_REASON_INVALID_PATH = 3;
  STORAGE_ERROR_REASON_MISSING_BLOB = 4;
  STORAGE_ERROR_REASON_UNVERIFIED_BLOB = 5;
  STORAGE_ERROR_REASON_REF_CONFLICT = 6;
  STORAGE_ERROR_REASON_INVARIANT_VIOLATION = 7;
  STORAGE_ERROR_REASON_UNAVAILABLE = 8;
}
```

`StorageErrorReason` should be carried in structured gRPC error details. Ref CAS
failures should map to a retryable `ABORTED` status, missing source objects to
`NOT_FOUND`, invariant failures to `INTERNAL`, and temporary backend failures to
`UNAVAILABLE`.

`PublishCommit` is the only normal operation that creates a commit and moves a
target ref. It runs as one metadata transaction:

```text
1. Verify every required blob is available.
2. Verify parent commits and root tree exist.
3. Verify expected ref still points at expected_old_commit_id.
4. Write commit metadata.
5. Move the ref with CAS.
6. Write outbox events for indexers.
```

If the ref CAS fails, the operation returns a retryable conflict and publishes
no commit. Higher-level services must rebase or reapply through the required
queue or queues.

The transaction interface is intentionally lower level than `PublishCommit`.
Most services should use specific storage operations rather than constructing
arbitrary metadata transactions. Administrative repair workflows may need the
lower-level primitive behind stricter authorization and audit logging.

Storage operations must be idempotent where practical. In particular, staging an
already available content hash should return the existing blob record, and
replaying outbox event writes for the same committed transaction must not create
duplicate logical events.

## 5. Blob And Metadata Transaction Semantics

Object storage systems are not part of the metadata transaction. The write
protocol must account for that.

Recommended staged write flow:

```text
1. Client uploads missing blob content by hash.
2. Server verifies hash and size.
3. Server marks blob records as staged or available.
4. Submit transaction writes tree nodes, commit metadata, and ref update.
5. After commit succeeds, referenced blobs are considered live.
6. Background GC removes unreferenced staged blobs after a grace period.
```

The metadata transaction must never point at a blob that has not been verified.

Blob upload can happen before submit. Commit publication happens only through
the metadata transaction and atomic ref update.

### 5.1 Distributed Garbage Collection

Garbage collection is a correctness feature, not only a cost-control feature.
The storage system must eventually remove abandoned staged blobs, obsolete
projection artifacts, and unreachable metadata without deleting anything that
can still be observed by a ref, changeset, cache lease, or repair workflow.

Reachability roots:

- accepted refs such as `refs/global/main`
- changeset patchset refs and open review state
- draft workspace snapshots with active leases
- staged blob leases that have not expired
- submit-queue items that are finalizing
- Git projection and packfile cache entries with active leases
- index repair checkpoints and replication watermarks

GC should run as a multi-phase mark, quarantine, and sweep workflow:

```text
1. Select a GC generation and stable metadata read watermark.
2. Enumerate reachability roots at or before that watermark.
3. Traverse commits, trees, blobs, patchsets, and registered cache artifacts.
4. Mark reachable object ids for the generation.
5. Quarantine unmarked candidates older than the grace period.
6. Recheck candidates against fresh roots before deletion.
7. Delete object-store bytes only after metadata references are gone or expired.
8. Emit audited deletion events.
```

The grace period must exceed expected replica lag, indexing lag, and client
upload retry windows. Staged blobs can use a shorter TTL, but available blobs
and metadata objects require reachability-based deletion. Projection caches and
derived indexes must register either a reachability root or an expiration time;
unregistered caches are disposable and must not be required for correctness.

Deletes must be idempotent. A failed or interrupted GC generation can be retried
without changing committed refs or visible changeset state.

## 6. Object Model

### 6.1 Blob

```text
Blob:
  id
  hash
  size
  compression
  storage_location
  state
```

Blobs are immutable and content-addressed.

### 6.2 Tree

```text
Tree:
  id
  hash
  entries_or_chunks[]
```

Trees are immutable.

### 6.3 Tree Entry

```text
TreeEntry:
  name
  kind
  mode
  tree_id
  blob_id
  symlink_target
  size
  content_hash
```

Supported entry kinds:

```text
file
directory
symlink
```

### 6.4 Commit

```text
Commit:
  id
  parent_ids
  root_tree_id
  author
  message
  created_at
  changed_paths[]
```

### 6.5 Ref

```text
Ref:
  name
  commit_id
  updated_at
  updated_by
```

## 7. Canonical Paths And Tree Hashing

The tree model must be deterministic across clients, operating systems, and
regions.

### 7.1 Path Rules

Canonical paths:

- are absolute
- start with a registered account slug, as `/{account}/...`
- do not use reserved top-level names such as `.gitslice`, `shared`, `system`,
  or `build`
- use `/` as the only separator
- are valid UTF-8
- are normalized to Unicode NFC
- do not contain empty segments
- do not contain `.` or `..` segments
- do not contain NUL
- are case-sensitive

Examples:

```text
valid:   /nicholas/app/README.md
invalid: nicholas/app/README.md
invalid: /nicholas/app/../secret.txt
invalid: /shared/lib
```

### 7.2 Entry Ordering

Directory entries are sorted by the byte order of their canonical UTF-8 names
after NFC normalization.

This ordering is used for:

- tree hashing
- directory pagination
- deterministic projection
- Git tree generation

### 7.3 Tree Hash Inputs

A tree entry hash includes:

```text
entry kind
entry name
mode
content hash or child tree hash
size where applicable
symlink target where applicable
```

Directory tree hashes are computed from ordered child entries.

The directory name itself is stored in the parent entry, not inside the child
tree. This allows a directory rename to reuse the child subtree hash.

### 7.4 File Modes

The initial mode model should support:

```text
regular file
executable file
directory
symlink
```

Additional platform-specific mode bits should not affect the canonical source
tree unless explicitly added to the model.

### 7.5 Huge Directory Handling

Very large directories must be chunked deterministically.

Recommended approach:

```text
DirectoryRoot
  -> DirectoryChunk[]
  -> ordered TreeEntry records
```

Chunking rules:

- chunks cover non-overlapping name ranges
- entries are ordered by canonical name
- chunk boundaries are deterministic for the same entry set
- chunk hashes feed into the directory root hash
- listing supports cursor-based pagination by entry name

This prevents one huge directory from becoming one massive metadata object while
keeping tree hashes deterministic.

### 7.6 Rename Behavior

Renaming a file changes the parent directory entries.

Renaming a directory changes the parent directory entry, but can reuse the
renamed directory's child tree because the child tree hash is independent of the
directory's name.

Path-based indexes still need to update affected path records after a rename.

## 8. Replication Architecture

Use regional read replicas and controlled write coordination.

```text
US primary
EU replica
Asia replica
```

Reads should be served locally when possible.

Writes should be coordinated through the region that owns the target ref and the
required account queue leases.

Blob replication can be lazy and demand-driven.

Metadata replication must preserve commit/ref consistency.

Ref updates must remain linearizable for queue target refs.

## 9. Storage Invariants

These invariants must not be violated:

```text
1. A committed tree is immutable.
2. A committed blob is immutable and content-addressed.
3. A commit points to exactly one root tree.
4. A ref update is atomic and conditional.
5. Metadata must never reference an unverified blob.
6. Derived indexes can be rebuilt from commits, trees, blobs, slice definitions, folder policy files, and queue definitions.
```
