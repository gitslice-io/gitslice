import type { AccountMembership } from "../api/types";
import { conversationsEnabled } from "./features";

// The viewer's place in an account, as the auth status reports it.
export interface Membership {
  account: string;
  // personal (the viewer's own), organization, or agent (an agent they claimed).
  kind: string;
  // owner, admin, writer, member, contributor or reader; "" when the server did not say.
  role: string;
}

export function normalizeMemberships(accounts: string[], memberships: AccountMembership[] | undefined): Membership[] {
  if (memberships && memberships.length > 0) {
    return memberships
      .filter((m) => m.account)
      .map((m) => ({ account: m.account ?? "", kind: m.kind ?? "", role: (m.role ?? "").toLowerCase() }));
  }
  // An older server lists only the names; the first is the personal account.
  return accounts.map((account, index) => ({ account, kind: index === 0 ? "personal" : "", role: "" }));
}

export function membershipFor(memberships: readonly Membership[] | undefined, account: string | undefined) {
  if (!memberships || memberships.length === 0) {
    return undefined;
  }
  const wanted = (account ?? "").trim().toLowerCase();
  if (!wanted) {
    return undefined;
  }
  return memberships.find((m) => m.account.toLowerCase() === wanted);
}

// Role hierarchy aligned with internal/authz/authz.go (design/36 §1).
export function roleTier(role: string | undefined): number {
  switch ((role ?? "").trim().toLowerCase()) {
    case "owner":
      return 5;
    case "admin":
      return 4;
    case "writer":
    case "member":
      return 3;
    case "contributor":
      return 2;
    case "reader":
      return 1;
    default:
      return 0;
  }
}

function accountRoleRank(role: string): number {
  switch (role) {
    case "owner":
      return 0;
    case "admin":
      return 1;
    case "writer":
      return 2;
    case "member":
      return 3;
    case "contributor":
      return 4;
    case "reader":
      return 5;
    default:
      return 6;
  }
}

export function higherRole(a: string | undefined, b: string | undefined): string {
  const normA = (a ?? "").trim().toLowerCase();
  const normB = (b ?? "").trim().toLowerCase();
  const tierA = roleTier(normA);
  const tierB = roleTier(normB);
  if (tierA === 0 && tierB === 0) {
    return "";
  }
  if (tierA !== tierB) {
    return tierA > tierB ? normA : normB;
  }
  return accountRoleRank(normB) < accountRoleRank(normA) ? normB : normA;
}

export function roleAllowsPropose(role: string | undefined): boolean {
  return roleTier(role) >= 2;
}

export function roleAllowsSubmit(role: string | undefined): boolean {
  return roleTier(role) >= 3;
}

export function roleAllowsReview(role: string | undefined): boolean {
  return roleTier(role) >= 3;
}

export function roleAllowsAdmin(role: string | undefined): boolean {
  return roleTier(role) >= 4;
}

// The same rules as the server (internal/authz): who may propose changesets,
// who may submit/review changesets, and who may administer slices/settings.
// A membership whose role an older server did not report is given the benefit
// of the doubt; the server still decides.
export function canPropose(membership: Membership | undefined): boolean {
  return Boolean(membership && (membership.role === "" || roleAllowsPropose(membership.role)));
}

export function canSubmit(membership: Membership | undefined): boolean {
  return Boolean(membership && (membership.role === "" || roleAllowsSubmit(membership.role)));
}

export function canReview(membership: Membership | undefined): boolean {
  return Boolean(membership && (membership.role === "" || roleAllowsReview(membership.role)));
}

export function canWrite(membership: Membership | undefined): boolean {
  return canSubmit(membership);
}

export function canAdmin(membership: Membership | undefined): boolean {
  return Boolean(membership && (membership.role === "" || roleAllowsAdmin(membership.role)));
}

export function kindLabel(kind: string) {
  switch (kind) {
    case "personal":
      return "Personal";
    case "organization":
      return "Organization";
    case "agent":
      return "Agent";
    default:
      return "Account";
  }
}

export const ACTIVE_ACCOUNT_KEY = "gitslice.activeAccount";

// accountHomeIsProfile says whether an account's Home is its profile page:
// an organization's or agent's Home has nothing the profile lacks unless
// conversations give it a feed (one page per account).
export function accountHomeIsProfile(kind: string) {
  return kind !== "personal" && !conversationsEnabled;
}
