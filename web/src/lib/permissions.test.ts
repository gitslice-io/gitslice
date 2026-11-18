import { describe, expect, it } from "vitest";

import { resolveChangesetPermissions, resolveSlicePermissions } from "./permissions";

describe("resolveSlicePermissions", () => {
  const paymentRef = { account: "acme", slice: "payment" };

  it("denies all write capabilities when signed out", () => {
    const perms = resolveSlicePermissions({
      isSignedIn: false,
      memberships: [{ account: "acme", kind: "organization", role: "owner" }],
      slice: paymentRef
    });
    expect(perms.canPropose).toBe(false);
    expect(perms.canSubmit).toBe(false);
    expect(perms.canReview).toBe(false);
    expect(perms.canAdmin).toBe(false);
    expect(perms.isAccountAdmin).toBe(false);
  });

  it("grants propose but not submit/review/admin to slice contributors (direct or @group share)", () => {
    const perms = resolveSlicePermissions({
      isSignedIn: true,
      memberships: [{ account: "nic", kind: "personal", role: "owner" }],
      sharedSlices: [
        {
          role: "contributor",
          slice: { id: "slice_acme_payment", ref: paymentRef }
        }
      ],
      slice: paymentRef
    });
    expect(perms.role).toBe("contributor");
    expect(perms.canPropose).toBe(true);
    expect(perms.canSubmit).toBe(false);
    expect(perms.canReview).toBe(false);
    expect(perms.canAdmin).toBe(false);
    expect(perms.isAccountAdmin).toBe(false);
    expect(perms.canSubmitToAccount("acme")).toBe(false);
  });

  it("combines account membership with higher shared slice role and distinguishes slice admin from account admin", () => {
    const perms = resolveSlicePermissions({
      isSignedIn: true,
      memberships: [
        { account: "nic", kind: "personal", role: "owner" },
        { account: "acme", kind: "organization", role: "reader" }
      ],
      sharedSlices: [
        {
          role: "admin",
          slice: { id: "slice_acme_payment", ref: paymentRef, formerNames: ["billing"] }
        }
      ],
      slice: { account: "acme", slice: "billing" }
    });
    expect(perms.role).toBe("admin");
    expect(perms.canPropose).toBe(true);
    expect(perms.canSubmit).toBe(true);
    expect(perms.canReview).toBe(true);
    expect(perms.canAdmin).toBe(true);
    expect(perms.canManageMembers).toBe(true);
    expect(perms.isAccountAdmin).toBe(false);
    expect(perms.canSubmitToAccount("acme")).toBe(true);
  });

  it("blocks propose/submit/review on archived slices while retaining admin read access", () => {
    const perms = resolveSlicePermissions({
      isSignedIn: true,
      memberships: [{ account: "acme", kind: "organization", role: "owner" }],
      slice: { id: "slice_1", ref: paymentRef, archived: true }
    });
    expect(perms.archived).toBe(true);
    expect(perms.canPropose).toBe(false);
    expect(perms.canSubmit).toBe(false);
    expect(perms.canReview).toBe(false);
    expect(perms.canManageMembers).toBe(false);
    expect(perms.canAdmin).toBe(true);
    expect(perms.isAccountAdmin).toBe(true);
  });
});

describe("resolveChangesetPermissions", () => {
  const baseChangeset = {
    id: "cs_1",
    author: "alice",
    authoringSlice: { account: "acme", slice: "payment" },
    status: "draft"
  };

  it("prevents non-owner slice writers from editing, submitting, or abandoning someone else's changeset", () => {
    const perms = resolveChangesetPermissions({
      isSignedIn: true,
      account: "bob",
      subjectId: "user_bob",
      memberships: [{ account: "acme", kind: "organization", role: "writer" }],
      changeset: baseChangeset
    });
    expect(perms.isOwner).toBe(false);
    expect(perms.canEditInfo).toBe(false);
    expect(perms.canAbandon).toBe(false);
    expect(perms.canSubmit).toBe(false);
    expect(perms.canReview).toBe(true);
  });

  it("allows changeset collaborators within their own slice role (contributor vs writer)", () => {
    const contributorCollab = resolveChangesetPermissions({
      isSignedIn: true,
      account: "bob-bot",
      subjectId: "agent_bob",
      memberships: [{ account: "bob-bot", kind: "personal", role: "owner" }],
      sharedSlices: [
        { role: "contributor", slice: { ref: { account: "acme", slice: "payment" } } }
      ],
      changeset: { ...baseChangeset, collaborators: ["bob-bot"] }
    });
    expect(contributorCollab.isCollaborator).toBe(true);
    expect(contributorCollab.isOwner).toBe(true);
    expect(contributorCollab.canEditInfo).toBe(true);
    expect(contributorCollab.canAbandon).toBe(true);
    expect(contributorCollab.canSubmit).toBe(false);

    const writerCollab = resolveChangesetPermissions({
      isSignedIn: true,
      account: "carol",
      subjectId: "user_carol",
      memberships: [{ account: "acme", kind: "organization", role: "writer" }],
      changeset: { ...baseChangeset, collaborators: ["carol"] }
    });
    expect(writerCollab.isCollaborator).toBe(true);
    expect(writerCollab.canEditInfo).toBe(true);
    expect(writerCollab.canAbandon).toBe(true);
    expect(writerCollab.canSubmit).toBe(true);
  });

  it("allows the human controller of an agent author via attribution.controllerUsername even without direct slice membership", () => {
    const perms = resolveChangesetPermissions({
      isSignedIn: true,
      account: "nic",
      subjectId: "user_nic",
      memberships: [{ account: "nic", kind: "personal", role: "owner" }],
      changeset: {
        ...baseChangeset,
        author: "web-agent",
        attribution: {
          actingSubjectId: "agent_web",
          actingUsername: "web-agent",
          controllerSubjectId: "user_nic",
          controllerUsername: "nic"
        }
      }
    });
    expect(perms.isController).toBe(true);
    expect(perms.isOwner).toBe(true);
    expect(perms.canEditInfo).toBe(true);
    expect(perms.canAbandon).toBe(true);
    expect(perms.canSubmit).toBe(true);
  });

  it("revokes original controller ownership after handoff to another author", () => {
    const perms = resolveChangesetPermissions({
      isSignedIn: true,
      account: "nic",
      subjectId: "user_nic",
      memberships: [{ account: "nic", kind: "personal", role: "owner" }],
      changeset: {
        ...baseChangeset,
        author: "bob",
        originalAuthor: "web-agent",
        attribution: {
          actingSubjectId: "agent_web",
          actingUsername: "web-agent",
          controllerSubjectId: "user_nic",
          controllerUsername: "nic"
        }
      }
    });
    expect(perms.isController).toBe(false);
    expect(perms.isOwner).toBe(false);
    expect(perms.canEditInfo).toBe(false);
    expect(perms.canAbandon).toBe(false);
    expect(perms.canSubmit).toBe(false);
  });
});
