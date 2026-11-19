import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { Changeset } from "../../api/types";
import { ApprovalsCard, ApprovalStatusBadge } from "./ApprovalsCard";
import { summarizeApprovals } from "./approvalSummary";

afterEach(cleanup);

describe("summarizeApprovals & ApprovalsCard", () => {
  it("shows unmet server approval requirements and lets a reviewer approve", () => {
    const onApprove = vi.fn();
    const cs: Changeset = {
      id: "cs_1",
      author: "bobby",
      currentPatchsetId: "ps_2",
      status: "draft",
      approvalStatus: {
        satisfied: false,
        requirements: [
          {
            kind: "slice_approvals",
            satisfied: true,
            required: 1,
            current: 1,
            label: "Approvals",
            detail: "Distinct non-author approvals required"
          },
          {
            kind: "gitslice_config",
            satisfied: false,
            required: 1,
            current: 0,
            label: "Config owner approval",
            detail: "/acme/app/.gitslice/triggers.yaml requires an owner or admin of acme"
          }
        ]
      },
      approvals: [
        { patchsetId: "ps_2", subjectId: "sub_carol", username: "carol" },
        { patchsetId: "ps_1", subjectId: "sub_old", username: "old_reviewer" }
      ]
    };

    const summary = summarizeApprovals(cs, { account: "alice", subjectId: "sub_alice" });
    expect(summary.satisfied).toBe(false);
    expect(summary.badgeTone).toBe("needed");
    expect(summary.badgeLabel).toBe("1 approval requirement needed");
    expect(summary.currentApprovals).toHaveLength(1);
    expect(summary.viewerHasApproved).toBe(false);

    render(
      <>
        <ApprovalStatusBadge summary={summary} />
        <ApprovalsCard canApprove onApprove={onApprove} summary={summary} />
      </>
    );

    expect(screen.getByText("1 approval requirement needed")).toBeInTheDocument();
    expect(screen.getByText("Waiting for approvals")).toBeInTheDocument();
    expect(screen.getByText("Config owner approval")).toBeInTheDocument();
    expect(screen.getByText("1/1")).toBeInTheDocument();
    expect(screen.getByText("0/1")).toBeInTheDocument();
    expect(screen.getByText("carol")).toBeInTheDocument();
    expect(screen.queryByText("old_reviewer")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(onApprove).toHaveBeenCalledTimes(1);
  });

  it("shows all requirements met and indicates when the viewer already approved", () => {
    const cs: Changeset = {
      id: "cs_2",
      author: "bobby",
      currentPatchsetId: "ps_1",
      status: "draft",
      approvalStatus: {
        satisfied: true,
        requirements: [
          {
            kind: "slice_human_approvals",
            satisfied: true,
            required: 1,
            current: 1,
            label: "Human approvals"
          }
        ]
      },
      approvals: [{ patchsetId: "ps_1", subjectId: "sub_alice", username: "alice" }]
    };

    const summary = summarizeApprovals(cs, { account: "alice", subjectId: "sub_alice" });
    expect(summary.satisfied).toBe(true);
    expect(summary.badgeLabel).toBe("Approved 1/1");
    expect(summary.viewerHasApproved).toBe(true);

    render(
      <>
        <ApprovalStatusBadge summary={summary} />
        <ApprovalsCard canApprove onApprove={vi.fn()} summary={summary} />
      </>
    );

    expect(screen.getByText("Approved 1/1")).toBeInTheDocument();
    expect(screen.getByText("All requirements met")).toBeInTheDocument();
    expect(screen.getByText("You approved this patchset")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("falls back to submitRequirements when approvalStatus is omitted", () => {
    const cs: Changeset = {
      id: "cs_3",
      author: "bobby",
      currentPatchsetId: "ps_1",
      status: "draft",
      submitRequirements: {
        requiredApprovals: 2,
        approvers: "@acme/reviewers"
      },
      approvals: [{ patchsetId: "ps_1", subjectId: "sub_carol", username: "carol" }]
    };

    const summary = summarizeApprovals(cs);
    expect(summary.hasRequirements).toBe(true);
    expect(summary.satisfied).toBe(false);
    expect(summary.badgeLabel).toBe("Needs approval (1/2)");
    expect(summary.requirements[0]?.label).toBe("Approvals from @acme/reviewers");
  });

  it("renders nothing in ApprovalsCard when there are no requirements and no approvals", () => {
    const summary = summarizeApprovals({
      id: "cs_4",
      status: "draft",
      approvalStatus: { satisfied: true, requirements: [] }
    });
    expect(summary.badgeTone).toBe("none");
    expect(summary.badgeLabel).toBe("No approvals required");
    const { container } = render(<ApprovalsCard summary={summary} />);
    expect(container).toBeEmptyDOMElement();
  });
});
