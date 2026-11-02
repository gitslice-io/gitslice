import { type SliceRef, type Int64String } from "./common";

export interface Slice {
  id?: string;
  ref?: SliceRef;
  definition?: SliceDefinition;
  definitionHash?: string;
  ciDaemonId?: string;
  // The name the CI daemon is shown by (its display name, else the name it
  // registered with); empty when the slice has none.
  ciDaemonName?: string;
  // Archived slices are read-only and left out of listings.
  archived?: boolean;
  // Names the slice had before it was renamed; they still open it.
  formerNames?: string[];
  // A short note shown on the slice's page; empty when there is none.
  description?: string;
  // A changeset whose base went stale is merged onto the latest base at
  // submit (a new patchset that reruns CI) instead of being refused.
  mergeOnSubmit?: boolean;
  // When the slice was created; set in listings only.
  createdAt?: string;
  // When a changeset authored from the slice last landed, else createdAt;
  // set in listings only.
  lastActivityAt?: string;
  // The slice's pool of CI daemons in order of preference (ciDaemonId is the
  // first), and their names.
  ciDaemonIds?: string[];
  ciDaemonNames?: string[];
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
  includeArchived?: boolean;
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
  // Alone, makes it the slice's only CI daemon ("" clears the pool).
  daemonId?: string;
  // Append it to the slice's pool of CI daemons, or take it out.
  add?: boolean;
  remove?: boolean;
}

export interface SetSliceMergeOnSubmitRequest {
  slice?: SliceRef;
  enabled?: boolean;
}

export interface SetSliceDescriptionRequest {
  slice?: SliceRef;
  description?: string;
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
