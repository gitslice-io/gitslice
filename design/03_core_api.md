# Gitslice Core API

This document defines the native gRPC API boundary for Gitslice core services.
Product context is in [00_product.md](00_product.md), the top-level
architecture is in
[01_gitslice_architecture_design.md](01_gitslice_architecture_design.md),
storage details are in [02_storage.md](02_storage.md), CLI behavior is in
[04_cli_design.md](04_cli_design.md), and Git compatibility behavior is in
[05_git_compatibility.md](05_git_compatibility.md).

## 1. API Principles

Native APIs are gRPC-first. HTTP and JSON endpoints should be generated through
grpc-gateway bindings where product or integration surfaces need them.

The core API should:

- expose slices, changesets, patchsets, refs, trees, and blobs as native objects
- keep normal writes changeset-oriented
- keep direct commit creation behind trusted internal service boundaries
- stream large file and blob payloads
- use canonical global paths at the API boundary
- return stable ids for commits, trees, blobs, patchsets, and changesets
- support CLI workspace hydration, diff validation, and optional operation
  recording without making local workspace state server-authoritative

Git compatibility remains a gateway concern. Git clients talk to the Git
gateway, and the gateway translates clone/fetch/push operations into these core
APIs.

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

service QueueService {
  rpc ResolveRequiredQueues(ResolveRequiredQueuesRequest) returns (ResolveRequiredQueuesResponse);
  rpc GetQueueItem(GetQueueItemRequest) returns (QueueItem);
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
  string id = 1;
  repeated string parent_ids = 2;
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
  string tree_id = 5;
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
}

enum Visibility {
  VISIBILITY_UNSPECIFIED = 0;
  VISIBILITY_PRIVATE = 1;
  VISIBILITY_ACCOUNT = 2;
  VISIBILITY_PUBLIC = 3;
}

message Roles {
  repeated string owners = 1;
  repeated string admins = 2;
  repeated string writers = 3;
  repeated string readers = 4;
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
  repeated SliceBinding slices = 2;
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
  SliceRef authoring_slice = 2;
  string base_commit_id = 3;
  repeated FileEdit file_edits = 4;
}

message ValidateWorkspaceDiffResponse {
  repeated string affected_paths = 1;
  repeated PathCoverage coverage = 2;
  repeated string required_policy_files = 3;
  repeated RequiredQueue required_queues = 4;
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
  repeated RequiredQueue required_queues = 10;
}

enum ChangesetStatus {
  CHANGESET_STATUS_UNSPECIFIED = 0;
  CHANGESET_STATUS_DRAFT = 1;
  CHANGESET_STATUS_REVIEW = 2;
  CHANGESET_STATUS_QUEUED = 3;
  CHANGESET_STATUS_SUBMITTING = 4;
  CHANGESET_STATUS_SUBMITTED = 5;
  CHANGESET_STATUS_ABANDONED = 6;
  CHANGESET_STATUS_FAILED = 7;
  CHANGESET_STATUS_NEEDS_REBASE = 8;
  CHANGESET_STATUS_MERGE_CONFLICT = 9;
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
  repeated string required_policy_files = 10;
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
  repeated string covering_slice_ids = 2;
  repeated string policy_file_paths = 3;
  repeated string policy_file_hashes = 4;
}

message RequiredQueue {
  string queue_id = 1;
  string queue_definition_hash = 2;
  string target_ref = 3;
}

message CreateChangesetRequest {
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

message ResolveRequiredQueuesRequest {
  SliceRef authoring_slice = 1;
  string target_ref = 2;
  repeated string changed_paths = 3;
}

message ResolveRequiredQueuesResponse {
  repeated RequiredQueue queues = 1;
}

message GetQueueItemRequest {
  string queue_id = 1;
  string changeset_id = 2;
}

message QueueItem {
  string queue_id = 1;
  string changeset_id = 2;
  int64 position = 3;
  bool runnable = 4;
}
```

## 3. Internal Commit API

Normal users should not create commits directly. Commit creation is an internal
service boundary used by submit workers after validation, queue leasing, checks,
and CAS preconditions have passed.

```proto
syntax = "proto3";

package gitslice.internal.v1;

service InternalCommitService {
  rpc CreateCommitFromPatchset(CreateCommitFromPatchsetRequest) returns (CreateCommitFromPatchsetResponse);
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
```

This API must not bypass validation for normal users. It should be reachable
only from trusted submit workers and administrative repair workflows.

## 4. Error Model

Core APIs should use canonical gRPC status codes:

- `INVALID_ARGUMENT` for malformed paths, invalid refs, or invalid request shape
- `NOT_FOUND` for missing slices, commits, refs, blobs, or changesets
- `PERMISSION_DENIED` for authorization failures
- `FAILED_PRECONDITION` for policy, queue, coverage, or stale patchset failures
- `ABORTED` for CAS failures and retryable submit races
- `RESOURCE_EXHAUSTED` for page-size, blob-size, or quota limits
- `INTERNAL` for invariant violations

Structured error details should include machine-readable reasons such as:

```text
PATH_OUTSIDE_AUTHORING_SLICE
POLICY_REQUIREMENTS_CHANGED
QUEUE_SELECTION_CHANGED
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

Direct pushes to protected refs should either be rejected or translated into
changesets according to the matching folder policy files for the pushed paths.
