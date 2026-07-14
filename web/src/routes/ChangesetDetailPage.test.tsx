import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor
} from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { Changeset, ChangesetStack, PatchsetConflict } from "../api/types";
import { ChangesetDetailPage } from "./ChangesetDetailPage";

const apiMock = vi.hoisted(() => ({
  current: {} as Record<string, unknown>
}));

const routerMock = vi.hoisted(() => {
  const listeners = new Set<() => void>();
  let search: Record<string, unknown> = {};
  return {
    back: vi.fn() as ReturnType<typeof vi.fn>,
    navigate: vi.fn() as ReturnType<typeof vi.fn>,
    params: {} as Record<string, string>,
    get search() {
      return search;
    },
    set search(next: Record<string, unknown>) {
      search = next;
    },
    // Mirror the router: replacing the search object notifies subscribers so
    // components reading it via useSearch re-render.
    setSearch(next: Record<string, unknown>) {
      search = next;
      listeners.forEach((listener) => listener());
    },
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    }
  };
});

vi.mock("../api/useApi", () => ({
  useApi: () => apiMock.current
}));

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => ({
    isLoaded: true,
    isSignedIn: true
  })
}));

vi.mock("@tanstack/react-router", async () => {
  const React = await import("react");
  return {
    Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
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

vi.mock("../components/diff/DiffViewer", () => ({
  DiffViewer: ({
    fileStates,
    onFileNeeded,
    onFileRetry
  }: {
    fileStates?: {
      path: string;
      status: string;
      changeKind?: string;
      file?: { changeKind?: string };
    }[];
    onFileNeeded?(path: string): void;
    onFileRetry?(path: string): void;
  }) => (
    <div data-testid="diff-viewer">
      Diff viewer
      {fileStates?.map((file, index) => (
        <div data-testid={`file-state-${index}`} key={file.path}>
          {file.path}: {file.status} kind=
          {file.file?.changeKind ?? file.changeKind ?? "unknown"}
          <button
            aria-label={`Need ${file.path}`}
            onClick={() => onFileNeeded?.(file.path)}
            type="button"
          >
            Need
          </button>
          {file.status === "error" ? (
            <button
              aria-label={`Retry ${file.path}`}
              onClick={() => onFileRetry?.(file.path)}
              type="button"
            >
              Retry
            </button>
          ) : null}
        </div>
      ))}
    </div>
  )
}));

describe("changeset detail page", () => {
  beforeEach(() => {
    // Apply the component's search update (the URL-driven from/to handles) and
    // notify subscribers, the way the real router would.
    routerMock.navigate = vi.fn((options?: { search?: unknown }) => {
      if (!options) {
        return;
      }
      const nextSearch =
        typeof options.search === "function"
          ? (options.search as (prev: Record<string, unknown>) => Record<
              string,
              unknown
            >)(routerMock.search)
          : (options.search as Record<string, unknown> | undefined);
      if (nextSearch !== undefined) {
        routerMock.setSearch(nextSearch);
      }
    });
    routerMock.back = vi.fn();
    routerMock.params = { id: "stk_parser" };
    routerMock.search = {};
    apiMock.current = makeApi();
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("shows base changeset and patchset diff controls on changeset detail", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = {};
    const api = makeApi();
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

    renderRoute(<ChangesetDetailPage />);

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

  it("drives the diff from URL from/to params on first render", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = { from: "ps_child_1", to: "ps_child_2" };
    const api = makeApi();
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

    renderRoute(<ChangesetDetailPage />);

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
    const api = makeApi();
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

    renderRoute(<ChangesetDetailPage />);

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
    const api = makeApi();
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

    renderRoute(<ChangesetDetailPage />);

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

  it("opens the conversation drawer from the URL", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = { conversation: "1" };
    const api = makeApi();
    const detail = changeset("cs_child", "use parser in payment API", {
      conversationId: "conv_child",
      conversationSeq: "7",
      patchsetId: "ps_child_1",
      patchsetNumber: "1"
    });
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    api.getConversationEvents = vi.fn().mockResolvedValue({
      events: [
        {
          conversationId: "conv_child",
          id: "event_1",
          role: "assistant",
          seq: "7",
          text: "Updated the parser.",
          type: "message"
        }
      ]
    });
    apiMock.current = api;

    renderRoute(<ChangesetDetailPage />);

    expect(
      await screen.findByRole("heading", { name: "Agent conversation" })
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(api.getConversationEvents).toHaveBeenCalledWith({
        conversationId: "conv_child",
        afterSeq: 0,
        beforeSeq: 7
      })
    );
    expect(await screen.findByText("Updated the parser.")).toBeInTheDocument();
  });

  it("preserves existing changeset search params when opening the conversation drawer", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = {
      file: "/acme/payment/parser.go",
      from: "ps_child_1",
      to: "ps_child_2"
    };
    const api = makeApi();
    const detail = changeset("cs_child", "use parser in payment API", {
      conversationId: "conv_child",
      conversationSeq: "9",
      patchsetId: "ps_child_2",
      patchsetNumber: "2"
    });
    detail.patchsets = [
      {
        ...(detail.patchsets?.[0] ?? {}),
        authoringConversationId: "conv_child",
        authoringConversationSeq: "3",
        createdAt: "2026-06-18T00:00:00Z",
        id: "ps_child_1",
        number: "1"
      },
      {
        ...(detail.patchsets?.[0] ?? {}),
        authoringConversationId: "conv_child",
        authoringConversationSeq: "9",
        createdAt: "2026-06-18T00:01:00Z",
        id: "ps_child_2",
        number: "2"
      }
    ];
    api.getChangeset = vi.fn().mockResolvedValue(detail);
    apiMock.current = api;

    renderRoute(<ChangesetDetailPage />);

    await screen.findByText("use parser in payment API");
    fireEvent.click(
      screen.getByRole("button", {
        name: "Toggle conversation for the selected patchset"
      })
    );

    expect(routerMock.search).toEqual({
      conversation: "1",
      file: "/acme/payment/parser.go",
      from: "ps_child_1",
      to: "ps_child_2"
    });

    fireEvent.click(
      screen.getByRole("button", {
        name: "Toggle conversation for the selected patchset"
      })
    );
    expect(routerMock.back).toHaveBeenCalledTimes(1);
  });

  it("shows pending glyph for files beyond eager window that have not loaded", async () => {
    routerMock.params = { id: "cs_large" };
    const api = makeApi();
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

    renderRoute(<ChangesetDetailPage />);

    await waitFor(() => expect(api.diffChangeset).toHaveBeenCalledTimes(10));
    // Files beyond the eager window (index >= 10) should not be requested yet
    expect(
      api.diffChangeset.mock.calls.some(([request]) => request.paths?.[0] === paths[10])
    ).toBe(false);
    // An upsert edit's kind is unknown until its diff loads — it must NOT be
    // presented as "modified".
    expect(screen.getByTestId("file-state-10")).toHaveTextContent(
      "kind=unknown"
    );
    await waitFor(() =>
      expect(screen.getByTestId("file-state-0")).toHaveTextContent(
        "kind=modified"
      )
    );
  });

  it("shows added glyph for lazily loaded files with new file mode header", async () => {
    routerMock.params = { id: "cs_large" };
    const api = makeApi();
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

    renderRoute(<ChangesetDetailPage />);

    await waitFor(() => expect(api.diffChangeset).toHaveBeenCalledTimes(10));
    
    // Request the 11th file
    fireEvent.click(screen.getByRole("button", { name: `Need ${eleventhPath}` }));

    await waitFor(() =>
      expect(screen.getByTestId("file-state-10")).toHaveTextContent("loaded")
    );

    // The parsed diff ("new file mode") is authoritative over the "upsert"
    // edit metadata: the file must present as added, not modified.
    expect(screen.getByTestId("file-state-10")).toHaveTextContent("kind=added");
    expect(api.diffChangeset).toHaveBeenCalledTimes(11);
    expect(api.diffChangeset).toHaveBeenLastCalledWith({
      changesetId: "cs_large",
      fromPatchset: undefined,
      paths: [eleventhPath],
      toPatchset: detail.currentPatchsetId
    });
  });

  it("shows dependent (child) changesets on changeset detail", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = {};
    const api = makeApi();
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

    renderRoute(<ChangesetDetailPage />);

    expect(
      await screen.findByText("use parser in payment API")
    ).toBeInTheDocument();
    // Exactly one dependent (cs_grandchild), alongside the base link to cs_root.
    expect(screen.getByText("Base changeset")).toBeInTheDocument();
    expect(await screen.findByText("Dependent")).toBeInTheDocument();
    expect(screen.getAllByText("Dependent")).toHaveLength(1);
    expect(api.listChangesets).toHaveBeenCalledWith({
      authoringSlice: { account: "acme", slice: "payment" },
      limit: 200
    });
  });

  it("shows guidance message for merge network errors", async () => {
    routerMock.params = { id: "cs_child" };
    routerMock.search = {};
    const api = makeApi();
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

    renderRoute(<ChangesetDetailPage />);

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
    const api = makeApi();
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

    renderRoute(<ChangesetDetailPage />);

    const mergeButton = await screen.findByRole("button", { name: "Merge" });
    fireEvent.click(mergeButton);

    expect(
      await screen.findByText("Changeset conflicts with base")
    ).toBeInTheDocument();
  });
});

function renderRoute(element: ReactElement) {
  const queryClient = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false }
    }
  });

  return render(
    <QueryClientProvider client={queryClient}>{element}</QueryClientProvider>
  );
}

function makeApi() {
  return {
    addStackEntry: vi.fn(),
    diffChangeset: vi.fn().mockResolvedValue({ changedPaths: [], diff: "" }),
    getChangeset: vi.fn().mockResolvedValue(
      changeset("cs_child", "use parser in payment API", {
        parentChangesetId: "cs_root",
        parentPatchsetId: "ps_root_1",
        stackId: "stk_parser"
      })
    ),
    getConversationEvents: vi.fn().mockResolvedValue({ events: [] }),
    getStack: vi.fn().mockResolvedValue(stackFixture()),
    listCheckRuns: vi.fn().mockResolvedValue({ runs: [] }),
    listChangesets: vi.fn().mockResolvedValue({ changesets: [] }),
    listDirectory: vi.fn().mockResolvedValue({
      entries: [
        {
          kind: "ENTRY_KIND_FILE",
          name: "parser.go",
          path: "/acme/payment/parser.go"
        }
      ]
    }),
    readFile: vi.fn().mockResolvedValue({ data: btoa("package payment\n") }),
    reparentStackEntry: vi.fn(),
    resolvePath: vi.fn().mockResolvedValue({
      entry: { kind: "ENTRY_KIND_DIRECTORY", path: "/" }
    }),
    resolveSlice: vi.fn().mockResolvedValue({ id: "slice_payment" }),
    restack: vi.fn(),
    streamCheckRun: vi.fn(async function* () {}),
    submitChangeset: vi.fn().mockResolvedValue({ changesetId: "cs_child" }),
    submitStack: vi.fn(),
    updateChangeset: vi.fn(),
    uploadBlob: vi.fn()
  };
}

function stackFixture(): ChangesetStack {
  return {
    activeEntryId: "cs_child",
    authoringSlice: { account: "acme", slice: "payment" },
    baseCommitId: "commit_base",
    createdAt: "2026-06-18T00:00:00Z",
    id: "stk_parser",
    rootEntryId: "cs_root",
    status: "open",
    targetRef: "refs/global/main",
    title: "payment parser rollout",
    updatedAt: "2026-06-18T00:01:00Z",
    entries: [
      {
        changeset: changeset("cs_root", "introduce parser", {
          patchsetId: "ps_root_1",
          patchsetNumber: "1"
        }),
        changesetId: "cs_root",
        depth: "0",
        displayOrder: "1",
        siblingOrder: "1",
        stackId: "stk_parser",
        state: "draft"
      },
      {
        changeset: changeset("cs_child", "use parser in payment API", {
          parentChangesetId: "cs_root",
          parentPatchsetId: "ps_root_1",
          patchsetId: "ps_child_1",
          patchsetNumber: "1",
          submitBlockedReason: "NeedsRestack"
        }),
        changesetId: "cs_child",
        depth: "1",
        displayOrder: "2",
        parentChangesetId: "cs_root",
        parentPatchsetId: "ps_root_1",
        siblingOrder: "1",
        stackId: "stk_parser",
        state: "needs_restack"
      },
      {
        changeset: changeset("cs_grandchild", "update tests for API behavior", {
          parentChangesetId: "cs_child",
          parentPatchsetId: "ps_child_1",
          patchsetId: "ps_grandchild_1",
          patchsetNumber: "1"
        }),
        changesetId: "cs_grandchild",
        depth: "2",
        displayOrder: "3",
        parentChangesetId: "cs_child",
        parentPatchsetId: "ps_child_1",
        siblingOrder: "1",
        stackId: "stk_parser",
        state: "draft"
      },
      {
        changeset: changeset("cs_sibling", "expose parser metrics", {
          parentChangesetId: "cs_root",
          parentPatchsetId: "ps_root_1",
          patchsetId: "ps_sibling_1",
          patchsetNumber: "1"
        }),
        changesetId: "cs_sibling",
        depth: "1",
        displayOrder: "4",
        parentChangesetId: "cs_root",
        parentPatchsetId: "ps_root_1",
        siblingOrder: "2",
        stackId: "stk_parser",
        state: "draft"
      }
    ]
  };
}

function changeset(
  id: string,
  title: string,
  options: {
    conversationId?: string;
    conversationSeq?: string;
    conflicts?: PatchsetConflict[];
    parentChangesetId?: string;
    parentPatchsetId?: string;
    patchsetId?: string;
    patchsetNumber?: string;
    stackId?: string;
    status?: string;
    submitBlockedReason?: string;
  } = {}
): Changeset {
  const patchsetId = options.patchsetId ?? `ps_${id}_1`;
  const patchsetNumber = options.patchsetNumber ?? "1";

  return {
    affectedPaths: [`/acme/payment/${id}.go`],
    author: "alice",
    authoringSlice: { account: "acme", slice: "payment" },
    baseCommitId: "commit_base",
    baseKind: options.parentChangesetId ? "patchset" : "commit",
    currentPatchsetId: patchsetId,
    currentPatchsetNumber: patchsetNumber,
    handle: `acme:payment@${id.replace("cs_", "")}`,
    id,
    parentChangesetId: options.parentChangesetId,
    parentPatchsetId: options.parentPatchsetId,
    patchsets: [
      {
        baseCommitId: "commit_base",
        baseKind: options.parentChangesetId ? "patchset" : "commit",
        basePatchsetId: options.parentPatchsetId,
        changedPaths: [`/acme/payment/${id}.go`],
        changesetId: id,
        conflicts: options.conflicts,
        authoringConversationId: options.conversationId,
        authoringConversationSeq: options.conversationSeq,
        fileEdits: [],
        id: patchsetId,
        number: patchsetNumber,
        resultTreeId: `tree_${id}`
      }
    ],
    stackId: options.stackId ?? "stk_parser",
    status: options.status ?? "draft",
    submitBlockedReason: options.submitBlockedReason,
    targetRef: "refs/global/main",
    title
  };
}
