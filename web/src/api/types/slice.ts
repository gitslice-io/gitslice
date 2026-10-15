import { type SliceRef, type Int64String } from "./common";

export interface Slice {
  id?: string;
  ref?: SliceRef;
  definition?: SliceDefinition;
  definitionHash?: string;
  ciDaemonId?: string;
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
  // personal or organization.
  accountKind?: string;
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

export interface SetSliceCIDaemonRequest {
  slice?: SliceRef;
  daemonId?: string;
}

/** A person or agent given a role on one slice (design/26_slice_members.md). */
export interface SliceMember {
  username?: string;
  subjectId?: string;
  /** admin, writer or reader. */
  role?: string;
  displayName?: string;
  /** person or agent. */
  kind?: string;
  addedBy?: string;
  createdAt?: string;
}

export interface ListSliceMembersRequest {
  slice?: SliceRef;
}

export interface ListSliceMembersResponse {
  slice?: SliceRef;
  members?: SliceMember[];
}

export interface SetSliceMemberRequest {
  slice?: SliceRef;
  username?: string;
  role?: string;
}

export interface RemoveSliceMemberRequest {
  slice?: SliceRef;
  /** Empty: the caller leaves. */
  username?: string;
}

export type ListSharedSlicesRequest = Record<string, never>;

export interface SharedSlice {
  slice?: Slice;
  role?: string;
}

export interface ListSharedSlicesResponse {
  slices?: SharedSlice[];
}
