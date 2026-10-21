import { type Int64String, type SliceRef } from "./common";

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
  conflicts?: PatchsetConflict[];
  kind?: string;
  baseKind?: string;
  basePatchsetId?: string;
  baseTreeId?: string;
  resultTreeId?: string;
  stackParentPatchsetId?: string;
  authoringConversationId?: string;
  authoringConversationSeq?: Int64String;
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
  stackId?: string;
  stackOrder?: Int64String;
  parentChangesetId?: string;
  parentPatchsetId?: string;
  baseKind?: string;
  stackDepth?: Int64String;
  siblingOrder?: Int64String;
}

export interface ChangesetStack {
  id?: string;
  authoringSlice?: SliceRef;
  targetRef?: string;
  baseCommitId?: string;
  title?: string;
  status?: string;
  activeEntryId?: string;
  rootEntryId?: string;
  entries?: ChangesetStackEntry[];
  createdBy?: string;
  createdAt?: string;
  updatedAt?: string;
}

export interface ChangesetStackEntry {
  stackId?: string;
  changesetId?: string;
  parentChangesetId?: string;
  parentPatchsetId?: string;
  siblingOrder?: Int64String;
  displayOrder?: Int64String;
  depth?: Int64String;
  state?: string;
  changeset?: Changeset;
}

export interface ListChangesetsRequest {
  authoringSlice?: SliceRef;
  status?: string;
  limit?: number;
  // Leave out patchsets (list views); see ListChangesetsRequest.summary.
  summary?: boolean;
}

export interface ListChangesetsResponse {
  changesets?: Changeset[];
}

export interface CreateStackRequest {
  authoringSlice?: SliceRef;
  targetRef?: string;
  baseCommitId?: string;
  title?: string;
}

export interface GetStackRequest {
  stackId?: string;
}

export interface ListStacksRequest {
  authoringSlice?: SliceRef;
  status?: string;
  limit?: number;
}

export interface ListStacksResponse {
  stacks?: ChangesetStack[];
}

export interface AddStackEntryRequest {
  stackId?: string;
  title?: string;
  description?: string;
  parentChangesetId?: string;
  parentPatchsetId?: string;
  siblingOrder?: Int64String;
}

export interface MoveStackEntryRequest {
  stackId?: string;
  changesetId?: string;
  siblingOrder?: Int64String;
}

export interface ReparentStackEntryRequest {
  stackId?: string;
  changesetId?: string;
  newParentChangesetId?: string;
  newParentPatchsetId?: string;
  siblingOrder?: Int64String;
}

export interface DetachStackEntryRequest {
  stackId?: string;
  changesetId?: string;
  title?: string;
}

export interface DetachStackEntryResponse {
  sourceStack?: ChangesetStack;
  detachedStack?: ChangesetStack;
}

export interface RestackRequest {
  stackId?: string;
  startChangesetId?: string;
  includeSiblings?: boolean;
  targetBaseCommitId?: string;
}

export interface RestackResponse {
  stackId?: string;
  entries?: Changeset[];
  status?: string;
}

export interface SubmitStackRequest {
  stackId?: string;
  subtreeRootChangesetId?: string;
}

export interface SubmitStackEntryResult {
  changesetId?: string;
  status?: string;
  commitId?: string;
  pendingPublishId?: string;
  blockedReason?: string;
}

export interface SubmitStackResponse {
  stackId?: string;
  status?: string;
  results?: SubmitStackEntryResult[];
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
  stackId?: string;
  parentChangesetId?: string;
  parentPatchsetId?: string;
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

export interface UpdateChangesetInfoRequest {
  changesetId?: string;
  title?: string;
  description?: string;
}

export interface UpdateChangesetRequest {
  changesetId?: string;
  expectedCurrentPatchsetId?: string;
  baseCommitId?: string;
  fileEdits?: FileEdit[];
  conflicts?: PatchsetConflict[];
  patchsetKind?: string;
  baseKind?: string;
  basePatchsetId?: string;
  expectedParentPatchsetId?: string;
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

export interface CopyPathRequest {
  // The slice the copy lands in; destinationPath must be inside it.
  slice?: SliceRef;
  sourcePath?: string;
  destinationPath?: string;
  // The slice the source is read through when it is outside slice.
  sourceSlice?: SliceRef;
}

export interface MovePathRequest {
  slice?: SliceRef;
  sourcePath?: string;
  destinationPath?: string;
}

export interface PathOperationResponse {
  changeset?: Changeset;
  commitId?: string;
}

export interface AbandonChangesetRequest {
  changesetId?: string;
  reason?: string;
}
