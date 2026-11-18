import { useMemo } from "react";
import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";

import type { Changeset, SharedSlice, Slice, SliceRef } from "../api/types";
import { useApi } from "../api/useApi";
import { sharedSlicesQuery } from "../queries/auth";
import { useSelection } from "../state/selection";
import {
  canAdmin,
  canSubmit,
  higherRole,
  membershipFor,
  normalizeMemberships,
  roleAllowsAdmin,
  roleAllowsPropose,
  roleAllowsReview,
  roleAllowsSubmit,
  type Membership
} from "./accounts";

export function useSharedSlices(enabled = true) {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  return useQuery({
    ...sharedSlicesQuery(api),
    enabled: Boolean(isLoaded && isSignedIn && enabled),
    staleTime: 60_000,
    retry: false
  });
}

interface NormalizedSliceTarget {
  id: string;
  account: string;
  slice: string;
  archived: boolean;
}

function isSliceRef(input: Slice | SliceRef): input is SliceRef {
  return "account" in input || "slice" in input;
}

function normalizeSliceTarget(input: Slice | SliceRef | undefined): NormalizedSliceTarget {
  if (!input) {
    return { id: "", account: "", slice: "", archived: false };
  }
  if (isSliceRef(input)) {
    return {
      id: "",
      account: (input.account ?? "").trim(),
      slice: (input.slice ?? "").trim(),
      archived: false
    };
  }
  return {
    id: (input.id ?? "").trim(),
    account: (input.ref?.account ?? "").trim(),
    slice: (input.ref?.slice ?? "").trim(),
    archived: Boolean(input.archived)
  };
}

function matchesSharedSlice(item: SharedSlice, target: NormalizedSliceTarget): boolean {
  const candidate = item.slice;
  if (!candidate) {
    return false;
  }
  if (target.id && candidate.id && candidate.id.trim() === target.id) {
    return true;
  }
  const candidateAccount = (candidate.ref?.account ?? "").trim().toLowerCase();
  const wantedAccount = target.account.toLowerCase();
  if (!wantedAccount || candidateAccount !== wantedAccount) {
    return false;
  }
  const wantedSlice = target.slice.toLowerCase();
  if (!wantedSlice) {
    return false;
  }
  if ((candidate.ref?.slice ?? "").trim().toLowerCase() === wantedSlice) {
    return true;
  }
  return Boolean(
    candidate.formerNames?.some((former) => former.trim().toLowerCase() === wantedSlice)
  );
}

export function sharedSliceRole(
  sharedSlices: readonly SharedSlice[] | undefined,
  slice: Slice | SliceRef | undefined
): string {
  const target = normalizeSliceTarget(slice);
  if (!sharedSlices || sharedSlices.length === 0 || (!target.id && !target.account)) {
    return "";
  }
  let best = "";
  for (const item of sharedSlices) {
    if (matchesSharedSlice(item, target)) {
      best = higherRole(best, item.role);
    }
  }
  return best;
}

export interface SlicePermissions {
  role: string;
  accountRole: string;
  sliceRole: string;
  archived: boolean;
  canPropose: boolean;
  canSubmit: boolean;
  canWrite: boolean;
  canReview: boolean;
  canAdmin: boolean;
  canManageMembers: boolean;
  isAccountAdmin: boolean;
  canSubmitToAccount(account: string): boolean;
}

export interface ResolveSlicePermissionsInput {
  isSignedIn: boolean;
  memberships?: readonly Membership[];
  sharedSlices?: readonly SharedSlice[];
  slice: Slice | SliceRef | undefined;
}

export function resolveSlicePermissions({
  isSignedIn,
  memberships,
  sharedSlices,
  slice
}: ResolveSlicePermissionsInput): SlicePermissions {
  const target = normalizeSliceTarget(slice);
  const canSubmitToAccount = (accountName: string): boolean => {
    if (!isSignedIn) {
      return false;
    }
    const wanted = accountName.trim().toLowerCase();
    if (!wanted) {
      return false;
    }
    if (canSubmit(membershipFor(memberships, wanted))) {
      return true;
    }
    return Boolean(
      sharedSlices?.some(
        (item) =>
          !item.slice?.archived &&
          (item.slice?.ref?.account ?? "").trim().toLowerCase() === wanted &&
          roleAllowsSubmit(item.role)
      )
    );
  };

  if (!isSignedIn || !target.account) {
    return {
      role: "",
      accountRole: "",
      sliceRole: "",
      archived: target.archived,
      canPropose: false,
      canSubmit: false,
      canWrite: false,
      canReview: false,
      canAdmin: false,
      canManageMembers: false,
      isAccountAdmin: false,
      canSubmitToAccount
    };
  }

  const accountMembership = membershipFor(memberships, target.account);
  // Preserve legacy fallback when an older server sends accounts without roles.
  const effectiveAccountRole = accountMembership
    ? accountMembership.role === ""
      ? "owner"
      : accountMembership.role.toLowerCase()
    : "";
  const sliceRole = sharedSliceRole(sharedSlices, slice);
  const role = higherRole(effectiveAccountRole, sliceRole);
  const isAccountAdmin = canAdmin(accountMembership);
  const sliceAdmin = roleAllowsAdmin(role);
  const archived = target.archived;
  const submitAllowed = !archived && roleAllowsSubmit(role);

  return {
    role,
    accountRole: accountMembership?.role ?? "",
    sliceRole,
    archived,
    canPropose: !archived && roleAllowsPropose(role),
    canSubmit: submitAllowed,
    canWrite: submitAllowed,
    canReview: !archived && roleAllowsReview(role),
    canAdmin: sliceAdmin,
    canManageMembers: !archived && sliceAdmin,
    isAccountAdmin,
    canSubmitToAccount
  };
}

export interface ChangesetPermissions {
  slice: SlicePermissions;
  isOwner: boolean;
  isAuthor: boolean;
  isCollaborator: boolean;
  isController: boolean;
  canPropose: boolean;
  canEditInfo: boolean;
  canAbandon: boolean;
  canSubmit: boolean;
  canReview: boolean;
  canAdmin: boolean;
}

export interface ResolveChangesetPermissionsInput {
  isSignedIn: boolean;
  account?: string;
  subjectId?: string;
  memberships?: readonly Membership[];
  sharedSlices?: readonly SharedSlice[];
  changeset: Changeset | undefined;
  slice?: Slice | SliceRef;
}

function sameIdentity(a: string | undefined, b: string | undefined): boolean {
  const left = (a ?? "").trim();
  const right = (b ?? "").trim();
  if (!left || !right) {
    return false;
  }
  return left.toLowerCase() === right.toLowerCase();
}

function matchesViewerIdentity(
  candidate: string | undefined,
  account: string | undefined,
  subjectId: string | undefined
): boolean {
  const trimmed = (candidate ?? "").trim();
  if (!trimmed) {
    return false;
  }
  if (subjectId && trimmed === subjectId.trim()) {
    return true;
  }
  if (account && trimmed.toLowerCase() === account.trim().toLowerCase()) {
    return true;
  }
  return false;
}

function isControllerOfChangesetAuthor(
  changeset: Changeset,
  account: string | undefined,
  subjectId: string | undefined,
  memberships: readonly Membership[] | undefined
): boolean {
  const author = (changeset.author ?? "").trim();
  if (!author) {
    return false;
  }

  const attr = changeset.attribution;
  if (
    attr &&
    (matchesViewerIdentity(attr.controllerUsername, account, subjectId) ||
      matchesViewerIdentity(attr.controllerSubjectId, account, subjectId))
  ) {
    const hasActing = Boolean(
      (attr.actingUsername ?? "").trim() || (attr.actingSubjectId ?? "").trim()
    );
    if (
      !hasActing ||
      sameIdentity(author, attr.actingUsername) ||
      sameIdentity(author, attr.actingSubjectId)
    ) {
      return true;
    }
  }

  for (const ps of changeset.patchsets ?? []) {
    if (!sameIdentity(ps.author, author) || !ps.attribution) {
      continue;
    }
    if (
      matchesViewerIdentity(ps.attribution.controllerUsername, account, subjectId) ||
      matchesViewerIdentity(ps.attribution.controllerSubjectId, account, subjectId)
    ) {
      return true;
    }
  }

  const agentMembership = membershipFor(memberships, author);
  return Boolean(agentMembership?.kind === "agent" && canAdmin(agentMembership));
}

export function resolveChangesetPermissions({
  isSignedIn,
  account,
  subjectId,
  memberships,
  sharedSlices,
  changeset,
  slice
}: ResolveChangesetPermissionsInput): ChangesetPermissions {
  const slicePerms = resolveSlicePermissions({
    isSignedIn,
    memberships,
    sharedSlices,
    slice: slice ?? changeset?.authoringSlice
  });

  if (!isSignedIn || !changeset) {
    return {
      slice: slicePerms,
      isOwner: false,
      isAuthor: false,
      isCollaborator: false,
      isController: false,
      canPropose: false,
      canEditInfo: false,
      canAbandon: false,
      canSubmit: false,
      canReview: false,
      canAdmin: slicePerms.canAdmin
    };
  }

  const isAuthor = matchesViewerIdentity(changeset.author, account, subjectId);
  const isCollaborator = Boolean(
    changeset.collaborators?.some((collab) =>
      matchesViewerIdentity(collab, account, subjectId)
    )
  );
  const isController =
    !isAuthor &&
    isControllerOfChangesetAuthor(changeset, account, subjectId, memberships);
  const isOwner = Boolean(
    isAuthor || isController || isCollaborator || slicePerms.canAdmin
  );

  let canPropose = false;
  let canSubmitChangeset = false;
  if (!slicePerms.archived) {
    if (slicePerms.canAdmin || isController) {
      canPropose = true;
      canSubmitChangeset = true;
    } else if (isAuthor || isCollaborator) {
      canPropose = slicePerms.canPropose;
      canSubmitChangeset = slicePerms.canSubmit;
    }
  }

  return {
    slice: slicePerms,
    isOwner,
    isAuthor,
    isCollaborator,
    isController,
    canPropose,
    canEditInfo: canPropose,
    canAbandon: canPropose,
    canSubmit: canSubmitChangeset,
    canReview: !slicePerms.archived && (slicePerms.canReview || isController),
    canAdmin: slicePerms.canAdmin
  };
}

export function useSlicePermissions(slice: Slice | SliceRef | undefined): SlicePermissions {
  const { isLoaded, isSignedIn } = useAuth();
  const selection = useSelection();
  const memberships = useMemo(
    () => selection.memberships ?? normalizeMemberships(selection.accounts ?? [], undefined),
    [selection.accounts, selection.memberships]
  );
  const signedIn = Boolean(isLoaded && isSignedIn);
  const sharedQuery = useSharedSlices(signedIn && Boolean(slice));

  return useMemo(
    () =>
      resolveSlicePermissions({
        isSignedIn: signedIn,
        memberships,
        sharedSlices: sharedQuery.data,
        slice
      }),
    [memberships, sharedQuery.data, signedIn, slice]
  );
}

export function useChangesetPermissions(
  changeset: Changeset | undefined,
  slice?: Slice | SliceRef
): ChangesetPermissions {
  const { isLoaded, isSignedIn } = useAuth();
  const selection = useSelection();
  const memberships = useMemo(
    () => selection.memberships ?? normalizeMemberships(selection.accounts ?? [], undefined),
    [selection.accounts, selection.memberships]
  );
  const account = selection.account ?? selection.accounts?.[0] ?? "";
  const subjectId = selection.subjectId ?? "";
  const signedIn = Boolean(isLoaded && isSignedIn);
  const targetSlice = slice ?? changeset?.authoringSlice;
  const sharedQuery = useSharedSlices(signedIn && Boolean(targetSlice));

  return useMemo(
    () =>
      resolveChangesetPermissions({
        isSignedIn: signedIn,
        account,
        subjectId,
        memberships,
        sharedSlices: sharedQuery.data,
        changeset,
        slice
      }),
    [account, changeset, memberships, sharedQuery.data, signedIn, slice, subjectId]
  );
}
