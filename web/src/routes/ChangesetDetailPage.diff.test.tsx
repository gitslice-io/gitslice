import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ChangesetDetailPage } from "./ChangesetDetailPage";
import {
  apiMock,
  changeset,
  cleanupChangesetTestHooks,
  makeChangesetApi,
  renderChangesetRoute,
  resetChangesetTestHooks,
  routerMock
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

describe("changeset detail diff controls and lazy fetching", () => {
  beforeEach(() => resetChangesetTestHooks());
  afterEach(() => cleanupChangesetTestHooks());

  it("shows base changeset and patchset diff controls on changeset detail", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = {};
    const api = makeChangesetApi();
    const detail = changeset("cs_child", "use parser in payment API", {
      parentChangesetId: "cs_root",
      parentPatchsetId: "ps_root_1",
      patchsetId: "ps_child_2",
      patchsetNumber: "2",
      stackId: "stk_parser"
    });
    detail.patchsets = [
      {
        ...(detail.patchsets?.[0] ?? {}),
        basePatchsetId: "ps_root_1",
        changedPaths: ["/acme/payment/parser.go"],
        createdAt: "2026-06-18T00:00:00Z",
        id: "ps_child_1",
        number: "1"
      },
      {
        ...(detail.patchsets?.[0] ?? {}),
        basePatchsetId: "ps_root_2",
        changedPaths: [
          "/acme/payment/parser.go",
          "/acme/payment/parser_test.go"
        ],
        createdAt: "2026-06-18T00:01:00Z",
        id: "ps_child_2",
        number: "2"
      }
    ];
    api.diffChangeset = vi.fn().mockResolvedValue({ changedPaths: [], diff: "" });
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    expect(await screen.findByText("use parser in payment API")).toBeInTheDocument();
    expect(screen.getByText("Base changeset")).toBeInTheDocument();
    expect(screen.queryByText(/^Dependencies /)).not.toBeInTheDocument();
    expect(screen.queryByText(/^dependencies /)).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Patchsets" })).toBeInTheDocument();
    expect(screen.getAllByText("Patchset 1").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Patchset 2").length).toBeGreaterThan(0);

    fireEvent.change(screen.getByLabelText("Diff base"), {
      target: { value: "ps_child_1" }
    });

    await waitFor(() =>
      expect(api.diffChangeset).toHaveBeenLastCalledWith({
        changesetId: "cs_child",
        fromPatchset: "ps_child_1",
        toPatchset: "ps_child_2"
      })
    );
    // The selection is captured in the URL so it is shareable / survives reload.
    expect(routerMock.navigate).toHaveBeenCalled();
    expect(routerMock.search).toEqual({ from: "ps_child_1" });
  });

  it("keeps the diff's unified/split mode in the URL, alongside other params", async () => {
    routerMock.params = { id: "cf52df6ccb" };
    routerMock.search = { diff: "split", file: "acme/payment/parser.go" };
    const api = makeChangesetApi();
    api.diffChangeset = vi.fn().mockResolvedValue({ changedPaths: [], diff: "" });
    api.getChangeset = vi
      .fn()
      .mockResolvedValue(changeset("cs_cf52df6ccbcb849a1b29b76c6b98335f", "add expenses"));
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    expect(await screen.findByTestId("diff-view-mode")).toHaveTextContent("split");

    fireEvent.click(screen.getByRole("button", { name: "Use unified" }));
    expect(routerMock.navigate).toHaveBeenLastCalledWith(
      expect.objectContaining({ replace: true, to: "/cs/$id" })
    );
    // Unified is the default, so it leaves the URL clean.
    expect(routerMock.search).toEqual({ diff: undefined, file: "acme/payment/parser.go" });
    expect(screen.getByTestId("diff-view-mode")).toHaveTextContent("unified");

    fireEvent.click(screen.getByRole("button", { name: "Use split" }));
    expect(routerMock.search).toEqual({ diff: "split", file: "acme/payment/parser.go" });
    expect(screen.getByTestId("diff-view-mode")).toHaveTextContent("split");
  });

  it("drives the diff from URL from/to params on first render", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = { from: "ps_child_1", to: "ps_child_2" };
    const api = makeChangesetApi();
    const detail = changeset("cs_child", "use parser in payment API", {
      patchsetId: "ps_child_2",
      patchsetNumber: "2",
      stackId: "stk_parser"
    });
    detail.patchsets = [
      {
        ...(detail.patchsets?.[0] ?? {}),
        createdAt: "2026-06-18T00:00:00Z",
        id: "ps_child_1",
        number: "1"
      },
      {
        ...(detail.patchsets?.[0] ?? {}),
        createdAt: "2026-06-18T00:01:00Z",
        id: "ps_child_2",
        number: "2"
      }
    ];
    api.diffChangeset = vi.fn().mockResolvedValue({ changedPaths: [], diff: "" });
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    await waitFor(() =>
      expect(api.diffChangeset).toHaveBeenCalledWith({
        changesetId: "cs_child",
        fromPatchset: "ps_child_1",
        toPatchset: "ps_child_2"
      })
    );
  });

  it("uses one unfiltered diff request for comparisons with at most 20 paths", async () => {
    routerMock.params = { id: "cs_small" };
    const api = makeChangesetApi();
    const detail = changeset("cs_small", "small diff");
    const paths = [
      "/acme/payment/alpha.go",
      "/acme/payment/beta.go"
    ];
    detail.patchsets![0].fileEdits = paths.map((path) => ({
      op: "upsert",
      path
    }));
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    api.diffChangeset = vi.fn().mockResolvedValue({ changedPaths: paths, diff: "" });
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    await waitFor(() => expect(api.diffChangeset).toHaveBeenCalledTimes(1));
    expect(api.diffChangeset).toHaveBeenCalledWith({
      changesetId: "cs_small",
      fromPatchset: undefined,
      toPatchset: detail.currentPatchsetId
    });
    expect(api.diffChangeset.mock.calls[0][0]).not.toHaveProperty("paths");
  });

  it("fetches large diffs per file, enables file 11 on demand, and retries errors", async () => {
    routerMock.params = { id: "cs_large" };
    const api = makeChangesetApi();
    const detail = changeset("cs_large", "large diff");
    const paths = Array.from(
      { length: 21 },
      (_, index) => `/acme/payment/file-${String(index + 1).padStart(2, "0")}.go`
    );
    detail.patchsets![0].changedPaths = paths;
    detail.patchsets![0].fileEdits = paths.map((path) => ({
      op: "upsert",
      path
    }));
    const eleventhPath = paths[10];
    let eleventhAttempts = 0;
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    api.diffChangeset = vi.fn().mockImplementation(
      async (request: { paths?: string[] }) => {
        const path = request.paths?.[0];
        if (!path) {
          throw new Error("full diff should not be requested");
        }
        if (path === eleventhPath && eleventhAttempts++ === 0) {
          throw new Error("temporary diff failure");
        }
        return {
          changedPaths: paths,
          diff: `diff --git a/${path} b/${path}\n--- a/${path}\n+++ b/${path}\n@@ -1 +1 @@\n-old\n+new\n`
        };
      }
    );
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    await waitFor(() => expect(api.diffChangeset).toHaveBeenCalledTimes(10));
    expect(
      api.diffChangeset.mock.calls.map(([request]) => request.paths)
    ).toEqual(paths.slice(0, 10).map((path) => [path]));
    expect(
      api.diffChangeset.mock.calls.some(([request]) => !request.paths?.length)
    ).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: `Need ${eleventhPath}` }));

    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: `Retry ${eleventhPath}` })
      ).toBeInTheDocument()
    );
    expect(api.diffChangeset).toHaveBeenCalledTimes(11);

    fireEvent.click(
      screen.getByRole("button", { name: `Retry ${eleventhPath}` })
    );

    await waitFor(() =>
      expect(screen.getByTestId("file-state-10")).toHaveTextContent("loaded")
    );
    expect(api.diffChangeset).toHaveBeenCalledTimes(12);
    expect(api.diffChangeset).toHaveBeenLastCalledWith({
      changesetId: "cs_large",
      fromPatchset: undefined,
      paths: [eleventhPath],
      toPatchset: detail.currentPatchsetId
    });
  });

  it("shows pending glyph for files beyond eager window that have not loaded", async () => {
    routerMock.params = { id: "cs_large" };
    const api = makeChangesetApi();
    const detail = changeset("cs_large", "large diff");
    const paths = Array.from(
      { length: 21 },
      (_, index) => `/acme/payment/file-${String(index + 1).padStart(2, "0")}.go`
    );
    detail.patchsets![0].changedPaths = paths;
    detail.patchsets![0].fileEdits = paths.map((path) => ({
      op: "upsert",
      path
    }));
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    api.diffChangeset = vi.fn().mockImplementation(
      async (request: { paths?: string[] }) => {
        const path = request.paths?.[0];
        if (!path) {
          throw new Error("full diff should not be requested");
        }
        return {
          changedPaths: paths,
          diff: `diff --git a/${path} b/${path}\n--- a/${path}\n+++ b/${path}\n@@ -1 +1 @@\n-old\n+new\n`
        };
      }
    );
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    await waitFor(() => expect(api.diffChangeset).toHaveBeenCalledTimes(10));
    expect(
      api.diffChangeset.mock.calls.some(([request]) => request.paths?.[0] === paths[10])
    ).toBe(false);
    expect(screen.getByTestId("file-state-10")).toHaveTextContent("kind=unknown");
    await waitFor(() =>
      expect(screen.getByTestId("file-state-0")).toHaveTextContent("kind=modified")
    );
  });

  it("shows added glyph for lazily loaded files with new file mode header", async () => {
    routerMock.params = { id: "cs_large" };
    const api = makeChangesetApi();
    const detail = changeset("cs_large", "large diff");
    const paths = Array.from(
      { length: 21 },
      (_, index) => `/acme/payment/file-${String(index + 1).padStart(2, "0")}.go`
    );
    detail.patchsets![0].changedPaths = paths;
    detail.patchsets![0].fileEdits = paths.map((path) => ({
      op: "upsert",
      path
    }));
    const eleventhPath = paths[10];
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    api.diffChangeset = vi.fn().mockImplementation(
      async (request: { paths?: string[] }) => {
        const path = request.paths?.[0];
        if (!path) {
          throw new Error("full diff should not be requested");
        }
        return {
          changedPaths: paths,
          diff: `diff --git a/${path} b/${path}\nnew file mode 100644\n--- /dev/null\n+++ b/${path}\n@@ -0,0 +1 @@\n+new content\n`
        };
      }
    );
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    await waitFor(() => expect(api.diffChangeset).toHaveBeenCalledTimes(10));

    fireEvent.click(screen.getByRole("button", { name: `Need ${eleventhPath}` }));

    await waitFor(() =>
      expect(screen.getByTestId("file-state-10")).toHaveTextContent("loaded")
    );

    expect(screen.getByTestId("file-state-10")).toHaveTextContent("kind=added");
    expect(api.diffChangeset).toHaveBeenCalledTimes(11);
    expect(api.diffChangeset).toHaveBeenLastCalledWith({
      changesetId: "cs_large",
      fromPatchset: undefined,
      paths: [eleventhPath],
      toPatchset: detail.currentPatchsetId
    });
  });
});

describe("reading a changed file in full", () => {
  beforeEach(() => resetChangesetTestHooks({ withApi: false, searchReplaceOnly: true }));

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("opens the original file, then the changed one, from the patchset's trees", async () => {
    const api = makeChangesetApi();
    const detail = changeset("cs_child", "use parser in payment API", {
      patchsetId: "ps_child_1",
      patchsetNumber: "1"
    });
    detail.authoringSlice = { account: "acme", slice: "payment" };
    detail.patchsets = [
      {
        ...(detail.patchsets?.[0] ?? {}),
        baseTreeId: "tree_base",
        changedPaths: ["/acme/payment/parser.go"],
        id: "ps_child_1",
        number: "1",
        resultTreeId: "tree_result"
      }
    ];
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    api.diffChangeset = vi.fn().mockResolvedValue({ changedPaths: [], diff: "" });
    api.readFile = vi.fn(({ rootTreeId }: { rootTreeId: string }) =>
      Promise.resolve({ data: btoa(rootTreeId === "tree_base" ? "package payment // before" : "package payment // after") })
    );
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);
    fireEvent.click(await screen.findByRole("button", { name: "View parser.go" }));

    expect(routerMock.search).toEqual({ side: "original", view: "acme/payment/parser.go" });
    const drawer = screen.getByRole("dialog", { name: "File" });
    await waitFor(() => expect(drawer).toHaveTextContent("package payment // before"));
    expect(api.readFile).toHaveBeenCalledWith({
      path: "/acme/payment/parser.go",
      rootTreeId: "tree_base",
      slice: { account: "acme", slice: "payment" }
    });
    expect(screen.getByRole("button", { name: "Original" })).toHaveAttribute("aria-pressed", "true");

    fireEvent.click(screen.getByRole("button", { name: "Changed" }));
    expect(routerMock.search).toEqual({ side: "changed", view: "acme/payment/parser.go" });
    await waitFor(() => expect(drawer).toHaveTextContent("package payment // after"));
    expect(api.readFile).toHaveBeenLastCalledWith({
      path: "/acme/payment/parser.go",
      rootTreeId: "tree_result",
      slice: { account: "acme", slice: "payment" }
    });
    expect(screen.getByText(/Open the latest version in acme\/payment/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(routerMock.back).toHaveBeenCalled();
  });

  it("opens a shared link to a deleted file on its original version", async () => {
    routerMock.search = { view: "/acme/payment/old.go" };
    const api = makeChangesetApi();
    const detail = changeset("cs_child", "drop old", { patchsetId: "ps_child_1", patchsetNumber: "1" });
    detail.authoringSlice = { account: "acme", slice: "payment" };
    detail.patchsets = [
      {
        ...(detail.patchsets?.[0] ?? {}),
        baseTreeId: "tree_base",
        changedPaths: ["/acme/payment/old.go"],
        fileEdits: [{ op: "delete", path: "/acme/payment/old.go" }],
        id: "ps_child_1",
        number: "1",
        resultTreeId: "tree_result"
      }
    ];
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    api.diffChangeset = vi.fn().mockResolvedValue({ changedPaths: [], diff: "" });
    api.readFile = vi.fn().mockResolvedValue({ data: btoa("package payment // old") });
    apiMock.current = api;

    renderChangesetRoute(<ChangesetDetailPage />);

    await waitFor(() =>
      expect(screen.getByRole("dialog", { name: "File" })).toHaveTextContent("package payment // old")
    );
    expect(api.readFile).toHaveBeenCalledWith(expect.objectContaining({ rootTreeId: "tree_base" }));
    expect(screen.getByRole("button", { name: "Changed" })).toBeDisabled();
    expect(screen.queryByText(/Open the latest version/)).not.toBeInTheDocument();
  });
});
