export type Int64String = string;
export type Base64String = string;

export interface Empty {}

export interface SliceRef {
  account?: string;
  slice?: string;
}

export type EntryKind =
  | "ENTRY_KIND_UNSPECIFIED"
  | "ENTRY_KIND_FILE"
  | "ENTRY_KIND_DIRECTORY"
  | "ENTRY_KIND_SYMLINK";

export interface TreeEntry {
  path?: string;
  name?: string;
  kind?: EntryKind;
  mode?: number;
  treeId?: string;
  blobId?: string;
  symlinkTarget?: string;
  size?: Int64String;
  contentHash?: string;
}

export interface GetAuthStatusRequest {}

export interface GetAuthStatusResponse {
  subjectId?: string;
  // Account slugs the subject belongs to; the first is their personal account.
  accounts?: string[];
}

export interface Ref {
  name?: string;
  commitId?: string;
  updatedAt?: string;
  updatedBy?: string;
}

export interface Commit {
  id?: string;
  parentIds?: string[];
  rootTreeId?: string;
  author?: string;
  message?: string;
  createdAt?: string;
  changedPaths?: string[];
}

export interface ResolvePathRequest {
  commitId?: string;
  path?: string;
}

export interface ResolvePathResponse {
  entry?: TreeEntry;
}

export interface ListDirectoryRequest {
  commitId?: string;
  path?: string;
  cursor?: string;
  pageSize?: number;
  slice?: SliceRef;
}

export interface ListDirectoryResponse {
  entries?: TreeEntry[];
  nextCursor?: string;
}

export interface ReadFileRequest {
  commitId?: string;
  path?: string;
  offset?: Int64String;
  length?: Int64String;
}

export interface ReadFileResponse {
  data?: Base64String;
  offset?: Int64String;
  contentHash?: string;
}

export interface GetCommitRequest {
  commitId?: string;
}

export interface ListCommitsRequest {
  refName?: string;
  limit?: number;
  path?: string;
  slice?: SliceRef;
  pageToken?: string;
  followMoves?: boolean;
}

export interface ListCommitsResponse {
  commits?: Commit[];
  nextPageToken?: string;
}

export interface GetRefRequest {
  refName?: string;
}

export interface GetBlobStatusRequest {
  contentHashes?: string[];
  slice?: SliceRef;
}

export interface BlobRecord {
  id?: string;
  contentHash?: string;
  size?: Int64String;
  storageLocation?: string;
  state?: string;
}

export interface GetBlobStatusResponse {
  blobs?: BlobRecord[];
}

export interface UploadBlobRequest {
  contentHash?: string;
  data?: Base64String;
  slice?: SliceRef;
}

export interface UploadBlobResponse {
  blobId?: string;
  contentHash?: string;
  size?: Int64String;
}

export interface Slice {
  id?: string;
  ref?: SliceRef;
  definition?: SliceDefinition;
  definitionHash?: string;
}

export interface SliceDefinition {
  sliceId?: string;
  version?: Int64String;
  includedPaths?: string[];
  visibility?: string;
  requiredApprovals?: number;
  requiredChecks?: string[];
}

export interface ResolveSliceRequest {
  ref?: SliceRef;
}

export interface GetSliceRequest {
  sliceId?: string;
}

export interface ListSlicesRequest {
  account?: string;
  cursor?: string;
  pageSize?: number;
}

export interface ListSlicesResponse {
  slices?: Slice[];
  nextCursor?: string;
}

export interface CreateSliceRequest {
  ref?: SliceRef;
  includedPaths?: string[];
  visibility?: string;
  requiredApprovals?: number;
  requiredChecks?: string[];
}

export interface UpdateSliceDefinitionRequest {
  sliceId?: string;
  expectedDefinitionHash?: string;
  definition?: SliceDefinition;
}

export interface SubmitRequirements {
  requiredApprovals?: number;
  requiredChecks?: string[];
  pathLockIds?: string[];
  sourceSliceDefinitionHash?: string;
  sourcePathLockSetHash?: string;
}

export interface FileEdit {
  op?: string;
  path?: string;
  oldPath?: string;
  blobId?: string;
  contentHash?: string;
  mode?: number;
}

export interface PathCoverage {
  path?: string;
  coveringSliceIds?: string[];
}

export interface PathBase {
  path?: string;
  baseCommitId?: string;
  exists?: boolean;
  entryKind?: string;
  mode?: number;
  blobId?: string;
  contentHash?: string;
  treeId?: string;
  symlinkTarget?: string;
  entryFingerprint?: string;
  check?: string;
}

export interface PathSetEntry {
  path?: string;
  recursive?: boolean;
}

export interface PatchsetConflict {
  path?: string;
  conflictClass?: string;
  oldBaseCommitId?: string;
  newBaseCommitId?: string;
  baseFingerprint?: string;
  localFingerprint?: string;
  remoteFingerprint?: string;
  baseContentHash?: string;
  localContentHash?: string;
  remoteContentHash?: string;
}

export interface Patchset {
  id?: string;
  changesetId?: string;
  number?: Int64String;
  baseCommitId?: string;
  author?: string;
  createdAt?: string;
  changedPaths?: string[];
  fileEdits?: FileEdit[];
  coverage?: PathCoverage[];
  submitRequirements?: SubmitRequirements;
  pathBases?: PathBase[];
  readSet?: PathSetEntry[];
  writeSet?: PathSetEntry[];
  handle?: string;
  conflicts?: PatchsetConflict[];
  kind?: string;
}

export interface Changeset {
  id?: string;
  authoringSlice?: SliceRef;
  author?: string;
  targetRef?: string;
  baseCommitId?: string;
  title?: string;
  description?: string;
  patchsets?: Patchset[];
  currentPatchsetId?: string;
  currentPatchsetNumber?: Int64String;
  status?: string;
  affectedPaths?: string[];
  submitRequirements?: SubmitRequirements;
  commitId?: string;
  pendingPublishId?: string;
  number?: Int64String;
  handle?: string;
  submitBlockedReason?: string;
}

export interface ListChangesetsRequest {
  authoringSlice?: SliceRef;
  status?: string;
  limit?: number;
}

export interface ListChangesetsResponse {
  changesets?: Changeset[];
}

export interface ApproveChangesetRequest {
  changesetId?: string;
}

export interface ApproveChangesetResponse {
  changesetId?: string;
  patchsetId?: string;
  subjectId?: string;
}

export interface CreateChangesetRequest {
  authoringSlice?: SliceRef;
  targetRef?: string;
  baseCommitId?: string;
  title?: string;
  description?: string;
}

export interface GetChangesetRequest {
  changesetId?: string;
}

export interface DiffChangesetRequest {
  changesetId?: string;
  patchset?: string;
  fromPatchset?: string;
  toPatchset?: string;
}

export interface DiffChangesetResponse {
  changesetId?: string;
  fromPatchsetId?: string;
  toPatchsetId?: string;
  changedPaths?: string[];
  diff?: string;
  changesetHandle?: string;
  fromPatchsetHandle?: string;
  toPatchsetHandle?: string;
}

export interface UpdateChangesetRequest {
  changesetId?: string;
  expectedCurrentPatchsetId?: string;
  baseCommitId?: string;
  fileEdits?: FileEdit[];
  conflicts?: PatchsetConflict[];
  patchsetKind?: string;
}

export interface SubmitChangesetRequest {
  changesetId?: string;
  expectedCurrentPatchsetId?: string;
}

export interface SubmitChangesetResponse {
  commitId?: string;
  targetRef?: string;
  newRefCommitId?: string;
  status?: string;
  pendingPublishId?: string;
  changesetHandle?: string;
}

export interface AbandonChangesetRequest {
  changesetId?: string;
  reason?: string;
}
