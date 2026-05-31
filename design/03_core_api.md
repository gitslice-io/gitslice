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

The concrete MVP proto used by the Go prototype lives under
[`../proto/core/v1/`](../proto/core/v1/). Those files are the implementation
source of truth for generated Go stubs, split by service boundary with shared
types in `common.proto`. The prototype currently keeps file/blob transfer unary
and uses string timestamps to keep the first end-to-end CLI/server path small;
the design target remains streaming payloads and typed protobuf timestamps once
larger-file behavior is implemented.

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
  rpc ResolveCommit(ResolveCommitRequest) returns (ResolveCommitResponse);
  rpc ListCommits(ListCommitsRequest) returns (ListCommitsResponse);
  rpc GetRef(GetRefRequest) returns (Ref);
  rpc ImportGitRepository(ImportGitRepositoryRequest) returns (ImportGitRepositoryResponse);
  rpc ImportGitRepositoryStream(ImportGitRepositoryRequest)
      returns (stream ImportGitRepositoryProgress);
}

service BlobService {
  rpc GetBlobStatus(GetBlobStatusRequest) returns (GetBlobStatusResponse);
  rpc UploadBlob(stream UploadBlobRequest) returns (UploadBlobResponse);
}

service FakeAccountService {
  rpc Login(LoginRequest) returns (LoginResponse);
  rpc ApproveSignup(ApproveSignupRequest) returns (ApproveSignupResponse);
}

service AuthService {
  rpc GetAuthStatus(GetAuthStatusRequest) returns (GetAuthStatusResponse);
}

service SliceService {
  rpc CreateSlice(CreateSliceRequest) returns (Slice);
  rpc ResolveSlice(ResolveSliceRequest) returns (Slice);
  rpc GetSlice(GetSliceRequest) returns (Slice);
  rpc ListSlices(ListSlicesRequest) returns (ListSlicesResponse);
  rpc UpdateSliceDefinition(UpdateSliceDefinitionRequest) returns (SliceDefinition);
  rpc DeleteSlice(DeleteSliceRequest) returns (DeleteSliceResponse);
}

service ChangesetService {
  rpc CreateChangeset(CreateChangesetRequest) returns (Changeset);
  rpc GetChangeset(GetChangesetRequest) returns (Changeset);
  rpc ListChangesets(ListChangesetsRequest) returns (ListChangesetsResponse);
  rpc DiffChangeset(DiffChangesetRequest) returns (DiffChangesetResponse);
  rpc UpdateChangeset(UpdateChangesetRequest) returns (Patchset);
  rpc SubmitChangeset(SubmitChangesetRequest) returns (SubmitChangesetResponse);
  rpc AbandonChangeset(AbandonChangesetRequest) returns (Empty);
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
  // Optional slice projection. When set, directory entries are filtered to
  // paths included by this slice while preserving canonical global paths.
  SliceRef slice = 5;
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
  // Canonical full native commit id.
  string commit_id = 1;
}

message ResolveCommitRequest {
  // Full id, sha256-prefixed short id, or bare short id.
  string commit_id = 1;
  // Optional target ref. Defaults to refs/global/main.
  string ref_name = 2;
  // Optional account-rooted file or directory path filter.
  string path = 3;
  // Optional slice projection. If path is also set, the server resolves within
  // the path/slice intersection.
  SliceRef slice = 4;
  // Optional move-following behavior. If unset, path history follows moves.
  optional bool follow_moves = 5;
}

message ResolveCommitResponse {
  Commit commit = 1;
  // Normalized prefix that matched the returned commit, for example
  // sha256:14e085c8afbf.
  string matched_prefix = 2;
}

message ListCommitsRequest {
  string ref_name = 1;
  int32 limit = 2;
  // Optional account-rooted file or directory path filter.
  string path = 3;
  // Optional slice projection. When set, commits are filtered to paths included
  // by the slice's current definition. If path is also set, the server returns
  // commits in the path/slice intersection.
  SliceRef slice = 4;
  // Optional opaque cursor returned by a previous ListCommitsResponse.
  string page_token = 5;
  // Optional move-following behavior. If unset, path history follows moves.
  optional bool follow_moves = 6;
}

message ListCommitsResponse {
  repeated Commit commits = 1;
  string next_page_token = 2;
}
```

When `path` is set, `ListCommits` returns path-filtered history. If
`follow_moves` is true, the server follows stable file and directory entity
identity across explicit moves and unambiguous inferred moves. Literal
path-only history is available with `follow_moves = false`. Slice-scoped
queries must not leak old or new paths outside the caller's visible projection.
The entity and move model is specified in
[13_file_identity_and_move_history.md](13_file_identity_and_move_history.md).

`GetCommit` is an exact lookup API and should require a canonical full native
commit id. Human-facing clients that accept abbreviated ids should call
`ResolveCommit` first. `ResolveCommit` normalizes these accepted input forms:

```text
sha256:<64hex>       full canonical id
sha256:<hex-prefix>  prefixed canonical id
<hex-prefix>         bare short id
```

The minimum accepted prefix length is 8 hex characters. Human CLI display should
default to 12 hex characters, but the resolver must still handle collisions.
Resolution is scoped by the same target ref, path, slice, account membership,
and move-following rules as `ListCommits`. It must decide uniqueness only within
the caller's visible commit set. A full id can use the `commits.id` primary key
for lookup, but the service still verifies that the returned commit is readable
before returning metadata or changed paths.

```proto
message GetRefRequest {
  string ref_name = 1;
}

message ImportGitRepositoryRequest {
  string source = 1;          // GitHub owner/repo shorthand, URL, or test file path.
  string mount_path = 2;      // Absolute account-rooted mount path.
  SliceRef authoring_slice = 3;
  string mode = 4;            // shallow or deep.
  string target_ref = 5;
  int32 max_commits = 6;      // Deep mode only; 0 means no limit.
  bool resume = 7;            // Reuse completed Git-to-native mappings.
}

message ImportedGitCommit {
  string git_commit_id = 1;
  string native_commit_id = 2;
  string message = 3;
}

message ImportGitRepositoryResponse {
  string source = 1;
  string mount_path = 2;
  string mode = 3;
  string target_ref = 4;
  string final_commit_id = 5;
  repeated ImportedGitCommit commits = 6;
}

message ImportGitRepositoryProgress {
  string phase = 1;              // cloning, listing_commits, reading_commit,
                                 // uploading_blobs, submitting, published, done.
  string message = 2;
  int64 current = 3;
  int64 total = 4;
  string git_commit_id = 5;
  string native_commit_id = 6;
  int32 changed_path_count = 7;
  ImportGitRepositoryResponse result = 8;
}

`ImportGitRepository` is retained for simple machine clients. The CLI should use
`ImportGitRepositoryStream` for interactive text output so large imports show
clone, commit enumeration, per-commit import, and publish progress.

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
  string current_changeset_handle = 7;
  string current_patchset_handle = 8;
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
  string changeset_handle = 10;
  string patchset_handle = 11;
}

message RecordWorkspaceOperationRequest {
  WorkspaceOperation operation = 1;
}

message RecordWorkspaceOperationResponse {
  string operation_id = 1;
}

message Changeset {
  // Canonical internal/API id. Human-facing clients should display handle.
  string id = 1;
  SliceRef authoring_slice = 2;
  string author = 3;
  string target_ref = 4;
  string base_commit_id = 5;
  string title = 6;
  string description = 7;
  repeated Patchset patchsets = 8;
  string current_patchset_id = 9;
  int64 current_patchset_number = 10;
  string status = 11;
  repeated string affected_paths = 12;
  SubmitRequirements submit_requirements = 13;
  string commit_id = 14;
  string pending_publish_id = 15;
  // Monotonic number allocated within authoring_slice.
  int64 number = 16;
  // Shareable user-facing selector, for example "acme/payment@42".
  string handle = 17;
}

message Patchset {
  // Canonical internal/API id. Human-facing clients should display number or
  // handle, not this raw id.
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
  // Shareable exact-version selector, for example "acme/payment@42.2".
  string handle = 14;
}

message FileEdit {
  string op = 1;       // upsert, delete, rename, mkdir
  string path = 2;
  string old_path = 3;
  string blob_id = 4;
  string content_hash = 5;
  uint32 mode = 6;
}

For `op = "rename"`, `old_path` and `path` describe a move of the same logical
entity. The server records the move as authoritative lineage and validates both
paths during submit.

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
  // Accepts either a canonical changeset id or a full shareable handle such as
  // "acme/payment@42". Workspace-relative forms are resolved by clients before
  // calling the API.
  string changeset_id = 1;
}

message ListChangesetsRequest {
  SliceRef authoring_slice = 1;
  string status = 2;
  int32 limit = 3;
}

message ListChangesetsResponse {
  repeated Changeset changesets = 1;
}

message DiffChangesetRequest {
  // Accepts either a canonical changeset id or a full shareable handle.
  string changeset_id = 1;
  // patchset, from_patchset, and to_patchset accept a patchset number encoded as
  // a string, a standalone exact-version handle like "acme/payment@42.2", or a
  // canonical patchset id for debugging/backward compatibility. Empty
  // patchset/to_patchset means the changeset's current patchset.
  string patchset = 2;
  string from_patchset = 3;
  string to_patchset = 4;
}

message DiffChangesetResponse {
  string changeset_id = 1;
  string from_patchset_id = 2;
  string to_patchset_id = 3;
  repeated string changed_paths = 4;
  string diff = 5;
  string changeset_handle = 6;
  string from_patchset_handle = 7;
  string to_patchset_handle = 8;
}

message UpdateChangesetRequest {
  // Accepts either a canonical changeset id or a full shareable handle.
  string changeset_id = 1;
  // Concurrency token from the current Changeset/Patchset response. This is not
  // a user-facing selector.
  string expected_current_patchset_id = 2;
  string base_commit_id = 3;
  // Every edit must be contained by the changeset's authoring slice.
  repeated FileEdit file_edits = 4;
}

message SubmitChangesetRequest {
  // Accepts either a canonical changeset id or a full shareable handle.
  string changeset_id = 1;
  // Concurrency token from the current Changeset/Patchset response. This is not
  // a user-facing selector.
  string expected_current_patchset_id = 2;
}

message SubmitChangesetResponse {
  string commit_id = 1;
  string target_ref = 2;
  string new_ref_commit_id = 3;
  string status = 4;
  string pending_publish_id = 5;
  string changeset_handle = 6;
}

message AbandonChangesetRequest {
  // Accepts either a canonical changeset id or a full shareable handle.
  string changeset_id = 1;
  string reason = 2;
}

```

`SubmitChangeset` returns `status = "pending_publish"` when the patchset has
passed path-head CAS admission but has not yet been published to the target ref.
In that state `commit_id` and `new_ref_commit_id` may be empty. Clients that
need root-visible state should poll `GetChangeset` until `status = "submitted"`
and then read the target ref.

Changeset APIs retain canonical `id` and `patchset_id` fields for storage,
idempotency, and concurrency tokens, but user-facing clients should display and
accept shareable handles. The stable changeset handle is
`account/slice@changeset_number`; the stable exact patchset handle is
`account/slice@changeset_number.patchset_number`. CLI and web clients may accept
workspace-relative shorthands such as `@42`, but must expand them before calling
the API. JSON responses should include both the handle and canonical id when an
object may be copied into another command.

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
- `FAILED_PRECONDITION` for submit requirement, coverage, stale patchset, or
  ambiguous commit-prefix failures
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
COMMIT_PREFIX_AMBIGUOUS
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
