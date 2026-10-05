import type { AccountMembership } from "../api/types";

// The viewer's place in an account, as the auth status reports it.
export interface Membership {
  account: string;
  // personal (the viewer's own), organization, or agent (an agent they claimed).
  kind: string;
  // owner, admin, writer, member or reader; "" when the server did not say.
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

export function membershipFor(memberships: readonly Membership[], account: string | undefined) {
  const wanted = (account ?? "").trim().toLowerCase();
  return memberships.find((m) => m.account.toLowerCase() === wanted);
}

const WRITE_ROLES = new Set(["owner", "admin", "member", "writer"]);
const ADMIN_ROLES = new Set(["owner", "admin"]);

// The same rules as the server (internal/authz): who may change a slice's
// files, and who may change its definition or create slices in the account.
// A membership whose role an older server did not report is given the benefit
// of the doubt; the server still decides.
export function canWrite(membership: Membership | undefined) {
  return Boolean(membership && (membership.role === "" || WRITE_ROLES.has(membership.role)));
}

export function canAdmin(membership: Membership | undefined) {
  return Boolean(membership && (membership.role === "" || ADMIN_ROLES.has(membership.role)));
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
