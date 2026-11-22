import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ChangesetDetailPage } from "./ChangesetDetailPage";
import {
  apiMock,
  changeset,
  cleanupChangesetTestHooks,
  makeChangesetApi,
  renderChangesetRoute,
  resetChangesetTestHooks,
  routerMock,
  selectionMock
} from "./changesetDetailTestHelpers";

vi.mock("../lib/features", async () => {
  const { featuresMock } = await import("./changesetDetailTestHelpers");
  return {
    get conversationsEnabled() {
      return featuresMock.conversationsEnabled;
    }
  };
});

vi.mock("../api/useApi", async () => {
  const { apiMock } = await import("./changesetDetailTestHelpers");
  return {
    useApi: () => apiMock.current
  };
});

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => ({
    isLoaded: true,
    isSignedIn: true
  })
}));

vi.mock("@tanstack/react-router", async () => {
  const [React, { makeRouterLink, routerMock }] = await Promise.all([
    import("react"),
    import("./changesetDetailTestHelpers")
  ]);
  return {
    Link: makeRouterLink,
    useNavigate: () => routerMock.navigate,
    useParams: () => routerMock.params,
    useRouter: () => ({ history: { back: routerMock.back, push: vi.fn() } }),
    useSearch: () =>
      React.useSyncExternalStore(
        routerMock.subscribe,
        () => routerMock.search,
        () => routerMock.search
      )
  };
});

vi.mock("../state/selection", async () => {
  const { selectionMock } = await import("./changesetDetailTestHelpers");
  return {
    useSelection: () => selectionMock.current
  };
});

vi.mock("../components/diff/DiffViewer", async () => {
  const { MockDiffViewer } = await import("./changesetDetailTestHelpers");
  return {
    DiffViewer: MockDiffViewer
  };
});

describe("changeset detail dependencies, errors, and review gating", () => {
  beforeEach(() => resetChangesetTestHooks());
  afterEach(() => cleanupChangesetTestHooks());

  it("shows dependent (child) changesets on changeset detail", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = {};
    const api = makeChangesetApi();
    api.getChangeset = vi.fn().mockResolvedValue(
      changeset("cs_child", "use parser in payment API", {
        parentChangesetId: "cs_root",
        parentPatchsetId: "ps_root_1"
      })
    );
    // cs_grandchild is based on cs_child; the slice's changeset list is how
    // dependents are resolved.
    api.listChangesets = vi.fn().mockResolvedValue({
      changesets: [
        changeset("cs_child", "use parser in payment API", {
          parentChangesetId: "cs_root"
        }),
        changeset("cs_grandchild", "update tests for API behavior", {
          parentChangesetId: "cs_child"
        }),
        changeset("cs_sibling", "expose parser metrics", {
          parentChangesetId: "cs_root"
        })
      ]
    });
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    expect(
      await screen.findByText("use parser in payment API")
    ).toBeInTheDocument();
    // Exactly one dependent (cs_grandchild), alongside the base link to cs_root.
    expect(screen.getByText("Base changeset")).toBeInTheDocument();
    expect(await screen.findByText("Dependent")).toBeInTheDocument();
    expect(screen.getAllByText("Dependent")).toHaveLength(1);
    expect(api.listChangesets).toHaveBeenCalledWith({
      authoringSlice: { account: "acme", slice: "payment" },
      limit: 200,
      summary: true
    });
  });

  it("shows guidance message for merge network errors", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = {};
    const api = makeChangesetApi();
    api.submitChangeset = vi.fn().mockRejectedValue(
      new TypeError("Failed to fetch")
    );
    api.getChangeset = vi.fn().mockResolvedValue(
      changeset("cs_child", "use parser in payment API", {
        patchsetId: "ps_child_1",
        patchsetNumber: "1"
      })
    );
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    const mergeButton = await screen.findByRole("button", { name: "Merge" });
    fireEvent.click(mergeButton);

    expect(
      await screen.findByText(
        "The merge request did not complete. Large changesets can take a couple of minutes — keep this tab in the foreground and try again."
      )
    ).toBeInTheDocument();
  });

  it("shows server error message for non-network merge errors", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = {};
    const api = makeChangesetApi();
    api.submitChangeset = vi.fn().mockRejectedValue(
      new Error("Changeset conflicts with base")
    );
    api.getChangeset = vi.fn().mockResolvedValue(
      changeset("cs_child", "use parser in payment API", {
        patchsetId: "ps_child_1",
        patchsetNumber: "1"
      })
    );
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    const mergeButton = await screen.findByRole("button", { name: "Merge" });
    fireEvent.click(mergeButton);

    expect(
      await screen.findByText("Changeset conflicts with base")
    ).toBeInTheDocument();
  });

  it("gates Merge, Abandon, and Edit on changeset ownership, collaborators, controller attribution, and contributor role", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = {};

    // 1. Non-owner writer on acme cannot merge, abandon, or edit alice's changeset.
    selectionMock.current = {
      account: "nic",
      accounts: ["nic", "acme"],
      memberships: [
        { account: "nic", kind: "personal", role: "owner" },
        { account: "acme", kind: "organization", role: "writer" }
      ],
      subjectId: "user_nic"
    };
    const api = makeChangesetApi();
    api.getChangeset = vi.fn().mockResolvedValue(changeset("cs_child", "alice work"));
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);
    expect(await screen.findByText("alice work")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Merge" })).toBeNull();
    expect(screen.queryByRole("button", { name: "More changeset actions" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    cleanup();

    // 2. Collaborator with shared contributor role can edit & abandon, but not merge.
    selectionMock.current = {
      account: "nic",
      accounts: ["nic"],
      memberships: [{ account: "nic", kind: "personal", role: "owner" }],
      subjectId: "user_nic"
    };
    const contribApi = makeChangesetApi();
    contribApi.listSharedSlices = vi.fn().mockResolvedValue({
      slices: [{ role: "contributor", slice: { ref: { account: "acme", slice: "payment" } } }]
    });
    contribApi.getChangeset = vi.fn().mockResolvedValue({
      ...changeset("cs_child", "collab work"),
      collaborators: ["nic"]
    });
    apiMock.current = contribApi;

    renderChangesetRoute(<ChangesetDetailPage />);
    expect(await screen.findByRole("button", { name: "Edit" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "More changeset actions" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Merge" })).toBeNull();
    cleanup();

    // 3. Human controller of an agent author gets Edit, Merge, and Abandon via attribution.controllerUsername.
    const ctrlApi = makeChangesetApi();
    ctrlApi.getChangeset = vi.fn().mockResolvedValue({
      ...changeset("cs_child", "agent work"),
      author: "web-agent",
      attribution: {
        actingSubjectId: "agent_web",
        actingUsername: "web-agent",
        controllerSubjectId: "user_nic",
        controllerUsername: "nic"
      }
    });
    apiMock.current = ctrlApi;

    renderChangesetRoute(<ChangesetDetailPage />);
    expect(await screen.findByRole("button", { name: "Merge" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "More changeset actions" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit" })).toBeInTheDocument();
  });
});
