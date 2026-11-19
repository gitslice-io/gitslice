import type { ApprovalRequirement, Changeset, ChangesetApproval } from "../../api/types";

export type ApprovalTone = "met" | "needed" | "approved" | "none";

export interface ApprovalSummary {
  badgeLabel: string;
  badgeTone: ApprovalTone;
  currentApprovals: ChangesetApproval[];
  hasRequirements: boolean;
  requirements: ApprovalRequirement[];
  satisfied: boolean;
  viewerHasApproved: boolean;
}

function matchesIdentity(
  candidate: string | undefined,
  account: string | undefined,
  subjectId: string | undefined
): boolean {
  const value = (candidate ?? "").trim();
  if (!value) return false;
  if (subjectId && value === subjectId.trim()) return true;
  if (account && value.toLowerCase() === account.trim().toLowerCase()) return true;
  return false;
}

function currentPatchsetApprovals(changeset: Changeset): ChangesetApproval[] {
  const approvals = changeset.approvals ?? [];
  const currentPs = (changeset.currentPatchsetId ?? "").trim();
  if (!currentPs) return approvals;
  return approvals.filter((app) => !app.patchsetId || app.patchsetId === currentPs);
}

function fallbackRequirements(
  changeset: Changeset,
  currentApprovals: ChangesetApproval[]
): ApprovalRequirement[] {
  const req = changeset.submitRequirements;
  if (!req) return [];

  const requiredApprovals = req.requiredApprovals ?? 0;
  const requiredHumanApprovals = req.requiredHumanApprovals ?? 0;
  const approversGroup = (req.approvers ?? "").trim();
  const author = (changeset.author ?? "").trim().toLowerCase();

  const nonAuthorApprovals = currentApprovals.filter((app) => {
    const user = (app.username || app.attribution?.actingUsername || app.subjectId || "")
      .trim()
      .toLowerCase();
    return !author || user !== author;
  });

  const out: ApprovalRequirement[] = [];
  if (requiredApprovals > 0) {
    const current = nonAuthorApprovals.length;
    out.push({
      kind: "slice_approvals",
      satisfied: current >= requiredApprovals,
      required: requiredApprovals,
      current,
      label: approversGroup ? `Approvals from ${approversGroup}` : "Approvals",
      detail: approversGroup
        ? `Distinct non-author approvals from ${approversGroup}`
        : "Distinct non-author approvals required"
    });
  }
  if (requiredHumanApprovals > 0) {
    const current = nonAuthorApprovals.filter((app) => {
      const acting = (app.attribution?.actingSubjectId ?? "").trim();
      const ctrl = (app.attribution?.controllerSubjectId ?? "").trim();
      return !ctrl || !acting || ctrl === acting;
    }).length;
    out.push({
      kind: "slice_human_approvals",
      satisfied: current >= requiredHumanApprovals,
      required: requiredHumanApprovals,
      current,
      label: approversGroup ? `Human approvals from ${approversGroup}` : "Human approvals",
      detail: approversGroup
        ? `Distinct human non-author approvals from ${approversGroup}`
        : "Distinct human non-author approvals required"
    });
  }
  return out;
}

export function summarizeApprovals(
  changeset: Changeset,
  viewer?: { account?: string; subjectId?: string }
): ApprovalSummary {
  const currentApprovals = currentPatchsetApprovals(changeset);
  const viewerHasApproved = Boolean(
    (viewer?.account || viewer?.subjectId) &&
      currentApprovals.some(
        (app) =>
          matchesIdentity(app.subjectId, viewer?.account, viewer?.subjectId) ||
          matchesIdentity(app.username, viewer?.account, viewer?.subjectId) ||
          matchesIdentity(app.attribution?.actingUsername, viewer?.account, viewer?.subjectId)
      )
  );

  const serverStatus = changeset.approvalStatus;
  const requirements = serverStatus
    ? (serverStatus.requirements ?? [])
    : fallbackRequirements(changeset, currentApprovals);
  const hasRequirements = requirements.length > 0;
  const satisfied = serverStatus
    ? Boolean(serverStatus.satisfied)
    : requirements.every((item) => Boolean(item.satisfied));

  let badgeTone: ApprovalTone = "none";
  let badgeLabel = "No approvals required";

  if (hasRequirements) {
    if (satisfied) {
      badgeTone = "met";
      if (requirements.length === 1) {
        const only = requirements[0];
        badgeLabel = `Approved ${only.current ?? 0}/${only.required ?? 0}`;
      } else {
        badgeLabel = "Approvals met";
      }
    } else {
      badgeTone = "needed";
      if (requirements.length === 1) {
        const only = requirements[0];
        badgeLabel = `Needs approval (${only.current ?? 0}/${only.required ?? 0})`;
      } else {
        const missing = requirements.filter((r) => !r.satisfied).length;
        badgeLabel = `${missing} approval requirement${missing === 1 ? "" : "s"} needed`;
      }
    }
  } else if (currentApprovals.length > 0) {
    badgeTone = "approved";
    badgeLabel = `Approved (${currentApprovals.length})`;
  }

  return {
    badgeLabel,
    badgeTone,
    currentApprovals,
    hasRequirements,
    requirements,
    satisfied,
    viewerHasApproved
  };
}
