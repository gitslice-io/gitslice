# Gitslice Core API

This document defines the native gRPC API boundary for Gitslice core services.
Product context is in [00_product.md](00_product.md), the top-level
architecture is in
[01_gitslice_architecture_design.md](01_gitslice_architecture_design.md),
storage details are in [02_storage.md](02_storage.md), CLI behavior is in
[04_cli_design.md](04_cli_design.md), and Git compatibility behavior is in
[05_git_compatibility.md](05_git_compatibility.md). Conflict resolution and
batched submit are in [07_conflict_resolution.md](07_conflict_resolution.md).

## 1. API Principles

Native APIs are gRPC-first. HTTP and JSON endpoints should be generated through
grpc-gateway bindings where product or integration surfaces need them.

The core API should:

- expose slices, changesets, patchsets, refs, trees, and blobs as native objects
- keep normal writes changeset-oriented
- keep direct commit creation behind trusted internal service boundaries
- stream large file and blob payloads
- use canonical global paths at the API boundary
- return content-addressed native ids for commits, trees, and blobs
- support CLI workspace hydration, diff validation, and optional operation
  recording without making local workspace state server-authoritative

Git compatibility remains a gateway concern. Git clients talk to the Git
gateway, and the gateway translates clone/fetch/push operations into these core
APIs. Git object ids are compatibility artifacts; native `commit_id`, `tree_id`,
and `blob_id` values are Gitslice content-addressed ids defined by the storage
layer.

## 2. Public Core Proto

The following proto shape is the starting contract. Some request and response
messages will grow as implementation details become concrete, but the service
boundaries should remain stable.

```proto
syntax = "proto3";

package gitslice.core.v1;

import "google/protobuf/timestamp.proto";

option go_package = "github.com/gitslice/gitslice/proto/core/v1;corev1";

service RepositoryService {
  rpc ResolvePath(ResolvePathRequest) returns (ResolvePathResponse);
  rpc ListDirectory(ListDirectoryRequest) returns (ListDirectoryResponse);
  rpc ReadFile(ReadFileRequest) returns (stream ReadFileResponse);
  rpc GetCommit(GetCommitRequest) returns (Commit);
  rpc GetRef(GetRefRequest) returns (Ref);
}

service BlobService {
  rpc GetBlobStatus(GetBlobStatusRequest) returns (GetBlobStatusResponse);
  rpc UploadBlob(stream UploadBlobRequest) returns (UploadBlobResponse);
}

service SliceService {
  rpc ResolveSlice(ResolveSliceRequest) returns (Slice);
  rpc GetSlice(GetSliceRequest) returns (Slice);
  rpc ListSlices(ListSlicesRequest) returns (ListSlicesResponse);
  rpc UpdateSliceDefinition(UpdateSliceDefinitionRequest) returns (SliceDefinition);
}

service ChangesetService {
  rpc CreateChangeset(CreateChangesetRequest) returns (Changeset);
  rpc GetChangeset(GetChangesetRequest) returns (Changeset);
  rpc UpdateChangeset(UpdateChangesetRequest) returns (Patchset);
  rpc SubmitChangeset(SubmitChangesetRequest) returns (SubmitChangesetResponse);
  rpc AbandonChangeset(AbandonChangesetRequest) returns (AbandonChangesetResponse);
}

service WorkspaceService {
  rpc GetWorkspaceState(GetWorkspaceStateRequest) returns (WorkspaceState);
  rpc HydratePaths(HydratePathsRequest) returns (stream HydratePathsResponse);
  rpc ValidateWorkspaceDiff(ValidateWorkspaceDiffRequest) returns (ValidateWorkspaceDiffResponse);
  rpc RecordWorkspaceOperation(RecordWorkspaceOperationRequest) returns (RecordWorkspaceOperationResponse);
}

message SliceRef {
  string account = 1;
  string slice = 2;
}

message CommitRef {
  string id = 1;
}

message Ref {
  string name = 1;
  string commit_id = 2;
  google.protobuf.Timestamp updated_at = 3;
  string updated_by = 4;
}

message Commit {
  // Native commit_id, not a Git object id.
  string id = 1;
  repeated string parent_ids = 2;
  // Native tree_id for the root tree of this commit.
  string root_tree_id = 3;
  string author = 4;
  string message = 5;
  google.protobuf.Timestamp created_at = 6;
  repeated string changed_paths = 7;
}

enum EntryKind {
  ENTRY_KIND_UNSPECIFIED = 0;
  ENTRY_KIND_FILE = 1;
  ENTRY_KIND_DIRECTORY = 2;
  ENTRY_KIND_SYMLINK = 3;
}

message TreeEntry {
  string path = 1;
  string name = 2;
  EntryKind kind = 3;
  uint32 mode = 4;
  // Native tree_id for directory entries.
  string tree_id = 5;
  // Native blob_id for file entries.
  string blob_id = 6;
  string symlink_target = 7;
  int64 size = 8;
  string content_hash = 9;
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

message ReadFileRequest {
  string commit_id = 1;
  string path = 2;
  int64 offset = 3;
  int64 length = 4;
}

message ReadFileResponse {
  bytes data = 1;
  int64 offset = 2;
  string content_hash = 3;
}

message GetCommitRequest {
  string commit_id = 1;
}

message GetRefRequest {
  string ref_name = 1;
}

message GetBlobStatusRequest {
  repeated string content_hashes = 1;
}

message BlobStatus {
  string content_hash = 1;
  bool available = 2;
  int64 size = 3;
}

message GetBlobStatusResponse {
  repeated BlobStatus blobs = 1;
}

message UploadBlobHeader {
  string content_hash = 1;
  int64 size = 2;
  string compression = 3;
}

message UploadBlobRequest {
  oneof part {
    UploadBlobHeader header = 1;
    bytes data = 2;
  }
}

message UploadBlobResponse {
  // Native blob_id, derived from the uploaded raw bytes.
  string blob_id = 1;
  string content_hash = 2;
  int64 size = 3;
}

message Slice {
  string id = 1;
  SliceRef ref = 2;
  SliceDefinition definition = 3;
}

message SliceDefinition {
  string slice_id = 1;
  int64 version = 2;
  string definition_hash = 3;
  string account = 4;
  string slug = 5;
  string display_name = 6;
  string default_branch = 7;
  Visibility visibility = 8;
  repeated string included_paths = 9;
  Roles roles = 10;
  SubmitSettings submit = 11;
}

enum Visibility {
  VISIBILITY_UNSPECIFIED = 0;
  VISIBILITY_PRIVATE = 1;
  VISIBILITY_ACCOUNT = 2;
  VISIBILITY_PUBLIC = 3;
}

// Roles reference immutable subject_id or account_id strings, not mutable
// usernames or slugs. Slugs are resolved at the API/CLI presentation layer.
message Roles {
  repeated string owner_ids = 1;
  repeated string admin_ids = 2;
  repeated string writer_ids = 3;
  repeated string reader_ids = 4;
}

message SubmitSettings {
  repeated string required_approvals = 1;
  repeated string required_checks = 2;
  bool allow_admin_override = 3;
}

message ResolveSliceRequest {
  SliceRef ref = 1;
}

message GetSliceRequest {
  string slice_id = 1;
}

message ListSlicesRequest {
  string account = 1;
  string cursor = 2;
  int32 page_size = 3;
}

message ListSlicesResponse {
  repeated Slice slices = 1;
  string next_cursor = 2;
}

message UpdateSliceDefinitionRequest {
  string slice_id = 1;
  string expected_definition_hash = 2;
  SliceDefinition definition = 3;
}

message WorkspaceRef {
  string id = 1;
}

message WorkspaceState {
  WorkspaceRef ref = 1;
  SliceBinding slice = 2;
  repeated string hydrated_paths = 3;
  string base_commit_id = 4;
  string current_changeset_id = 5;
  string current_patchset_id = 6;
}

message SliceBinding {
  SliceRef slice = 1;
  string slice_id = 2;
  string slice_definition_hash = 3;
}

message GetWorkspaceStateRequest {
  WorkspaceRef workspace = 1;
}

message HydratePathsRequest {
  WorkspaceRef workspace = 1;
  repeated string paths = 2;
  HydrationMode mode = 3;
}

enum HydrationMode {
  HYDRATION_MODE_UNSPECIFIED = 0;
  HYDRATION_MODE_FILE_CONTENTS = 1;
  HYDRATION_MODE_METADATA_ONLY = 2;
}

message HydratePathsResponse {
  string path = 1;
  TreeEntry entry = 2;
  bytes data = 3;
}

message ValidateWorkspaceDiffRequest {
  WorkspaceRef workspace = 1;
  // The workspace's bound slice is the authoring slice for the proposed
  // changeset. The server rejects file_edits outside that slice.
  string base_commit_id = 2;
  repeated FileEdit file_edits = 3;
}

message ValidateWorkspaceDiffResponse {
  repeated string affected_paths = 1;
  repeated PathCoverage coverage = 2;
  SubmitRequirements submit_requirements = 3;
  repeated PathBase path_bases = 4;
  repeated PathSetEntry read_set = 5;
  repeated PathSetEntry write_set = 6;
}

message WorkspaceOperation {
  string id = 1;
  WorkspaceRef workspace = 2;
  string operation_type = 3;
  string description = 4;
  google.protobuf.Timestamp created_at = 5;
  string actor = 6;
  repeated string affected_paths = 7;
  string changeset_id = 8;
  string patchset_id = 9;
}

message RecordWorkspaceOperationRequest {
  WorkspaceOperation operation = 1;
}

message RecordWorkspaceOperationResponse {
  string operation_id = 1;
}

message Changeset {
  string id = 1;
  SliceRef authoring_slice = 2;
  string author = 3;
  string target_ref = 4;
  string base_commit_id = 5;
  repeated Patchset patchsets = 6;
  int64 current_patchset_number = 7;
  ChangesetStatus status = 8;
  repeated string affected_paths = 9;
  SubmitRequirements submit_requirements = 10;
}

enum ChangesetStatus {
  CHANGESET_STATUS_UNSPECIFIED = 0;
  CHANGESET_STATUS_DRAFT = 1;
  CHANGESET_STATUS_REVIEW = 2;
  CHANGESET_STATUS_SUBMITTING = 3;
  CHANGESET_STATUS_SUBMITTED = 4;
  CHANGESET_STATUS_ABANDONED = 5;
  CHANGESET_STATUS_FAILED = 6;
  CHANGESET_STATUS_NEEDS_REBASE = 7;
  CHANGESET_STATUS_MERGE_CONFLICT = 8;
  CHANGESET_STATUS_NEEDS_REQUIREMENT_REFRESH = 9;
}

message Patchset {
  string id = 1;
  string changeset_id = 2;
  int64 number = 3;
  string base_commit_id = 4;
  string author = 5;
  google.protobuf.Timestamp created_at = 6;
  repeated string changed_paths = 7;
  repeated FileEdit file_edits = 8;
  repeated PathCoverage coverage = 9;
  SubmitRequirements submit_requirements = 10;
  repeated PathBase path_bases = 11;
  repeated PathSetEntry read_set = 12;
  repeated PathSetEntry write_set = 13;
}

message FileEdit {
  FileEditOp op = 1;
  string path = 2;
  string old_path = 3;
  string staged_blob_id = 4;
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

message PathCoverage {
  string path = 1;
  // Informational coverage snapshot for overlap, projection invalidation, and
  // conflict reporting. It does not make the changeset multi-slice and does not
  // add approval requirements beyond the authoring slice and active path locks.
  repeated string covering_slice_ids = 2;
}

message PathBase {
  string path = 1;
  string base_commit_id = 2;
  bool exists = 3;
  EntryKind entry_kind = 4;
  uint32 mode = 5;
  string blob_id = 6;
  string content_hash = 7;
  string tree_id = 8;
  string symlink_target = 9;
  string entry_fingerprint = 10;
  PathBaseCheck check = 11;
}

enum PathBaseCheck {
  PATH_BASE_CHECK_UNSPECIFIED = 0;
  PATH_BASE_CHECK_EXACT_ENTRY = 1;
  PATH_BASE_CHECK_MUST_BE_MISSING = 2;
  PATH_BASE_CHECK_MUST_EXIST_DIRECTORY = 3;
}

message PathSetEntry {
  string path = 1;
  bool recursive = 2;
}

message SubmitRequirements {
  repeated string required_approvals = 1;
  repeated string required_checks = 2;
  repeated string path_lock_ids = 3;
  string source_slice_definition_hash = 4;
  string source_path_lock_set_hash = 5;
}

message CreateChangesetRequest {
  // Exactly one authoring slice. There is intentionally no secondary-slice or
  // linked-changeset field in the MVP API.
  SliceRef authoring_slice = 1;
  string target_ref = 2;
  string base_commit_id = 3;
  string title = 4;
  string description = 5;
}

message GetChangesetRequest {
  string changeset_id = 1;
}

message UpdateChangesetRequest {
  string changeset_id = 1;
  string expected_current_patchset_id = 2;
  string base_commit_id = 3;
  // Every edit must be contained by the changeset's authoring slice.
  repeated FileEdit file_edits = 4;
}

message SubmitChangesetRequest {
  string changeset_id = 1;
  string expected_current_patchset_id = 2;
}

message SubmitChangesetResponse {
  string commit_id = 1;
  string target_ref = 2;
  string new_ref_commit_id = 3;
}

message AbandonChangesetRequest {
  string changeset_id = 1;
  string reason = 2;
}

message AbandonChangesetResponse {}

```

## 3. Internal Commit API

Normal users should not create commits directly. Commit creation is an internal
service boundary used by submit workers after validation, required checks, and
CAS preconditions have passed.

```proto
syntax = "proto3";

package gitslice.internal.v1;

service InternalCommitService {
  rpc CreateCommitFromPatchset(CreateCommitFromPatchsetRequest) returns (CreateCommitFromPatchsetResponse);
  rpc CreateCommitBatchFromPatchsets(CreateCommitBatchFromPatchsetsRequest) returns (CreateCommitBatchFromPatchsetsResponse);
}

message CreateCommitFromPatchsetRequest {
  string changeset_id = 1;
  string patchset_id = 2;
  string target_ref = 3;
  string expected_old_commit_id = 4;
  string author = 5;
  string message = 6;
}

message CreateCommitFromPatchsetResponse {
  string commit_id = 1;
  string new_ref_commit_id = 2;
}

message PatchsetCommitInput {
  string changeset_id = 1;
  string patchset_id = 2;
  string author = 3;
  string message = 4;
}

message CreateCommitBatchFromPatchsetsRequest {
  string target_ref = 1;
  string expected_old_commit_id = 2;
  repeated PatchsetCommitInput commits = 3;
}

message PublishedPatchsetCommit {
  string changeset_id = 1;
  string patchset_id = 2;
  string commit_id = 3;
}

message CreateCommitBatchFromPatchsetsResponse {
  repeated PublishedPatchsetCommit commits = 1;
  string new_ref_commit_id = 2;
}
```

This API must not bypass validation for normal users. It should be reachable
only from trusted submit workers and administrative repair workflows. Batch
creation is valid only after the submit service has proven that candidate
read/write sets are compatible and read-set predicates are fresh for the
target-ref head being updated.

## 4. Error Model

Core APIs should use canonical gRPC status codes:

- `INVALID_ARGUMENT` for malformed paths, invalid refs, or invalid request shape
- `NOT_FOUND` for missing slices, commits, refs, blobs, or changesets
- `PERMISSION_DENIED` for authorization failures
- `FAILED_PRECONDITION` for submit requirement, coverage, or stale patchset failures
- `ABORTED` for CAS failures and retryable submit races
- `RESOURCE_EXHAUSTED` for page-size, blob-size, or quota limits
- `INTERNAL` for invariant violations

Structured error details should include machine-readable reasons such as:

```text
PATH_OUTSIDE_AUTHORING_SLICE
MULTI_SLICE_CHANGESET_UNSUPPORTED
PATH_BASE_STALE
SUBMIT_REQUIREMENTS_CHANGED
REF_CAS_FAILED
PATCHSET_STALE
MISSING_BLOB
```

## 5. Gateway Notes

HTTP and JSON APIs can be exposed through grpc-gateway for browser and SDK
clients. The gRPC API remains the source contract.

The Git gateway is separate. Its detailed behavior is defined in
[05_git_compatibility.md](05_git_compatibility.md). At the API boundary it should:

```text
Git URL
  -> ResolveSlice
  -> RepositoryService reads for clone/fetch
  -> ChangesetService writes for push-to-changeset
```

Direct pushes to protected refs must be intercepted by the Git gateway and
routed through the changeset merge path. The gateway must create or update a
changeset via `ChangesetService.CreateChangeset` or
`ChangesetService.UpdateChangeset`, generate a patchset, and run the same
submit validation pipeline as native CLI writes. The gateway should return a
message informing the user that their push was converted to a changeset.
