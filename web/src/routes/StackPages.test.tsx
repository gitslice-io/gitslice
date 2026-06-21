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
import { StackDetailPage } from "./StackDetailPage";
import { StackRestackPage } from "./StackRestackPage";
import { StackSubmitPage } from "./StackSubmitPage";

const apiMock = vi.hoisted(() => ({
  current: {} as Record<string, unknown>
}));

const routerMock = vi.hoisted(() => ({
  navigate: vi.fn(),
  params: {} as Record<string, string>,
  search: {} as Record<string, unknown>
}));

vi.mock("../api/useApi", () => ({
  useApi: () => apiMock.current
}));

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => ({
    isLoaded: true,
    isSignedIn: true
  })
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
  useNavigate: () => routerMock.navigate,
  useParams: () => routerMock.params,
  useSearch: () => routerMock.search
}));

vi.mock("../components/diff/DiffViewer", () => ({
  DiffViewer: () => <div data-testid="diff-viewer">Diff viewer</div>
}));

describe("dependency route pages", () => {
  beforeEach(() => {
    routerMock.navigate = vi.fn();
    routerMock.params = { id: "stk_parser" };
    routerMock.search = {};
    apiMock.current = makeApi();
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it("renders dependency order, base links, active detail, and separate actions", async () => {
    renderRoute(<StackDetailPage />);

    expect(await screen.findByText("payment parser rollout")).toBeInTheDocument();
    expect(screen.getByText("Changesets")).toBeInTheDocument();
    expect(screen.getByText("introduce parser")).toBeInTheDocument();
    expect(screen.getAllByText("use parser in payment API").length).toBeGreaterThan(0);
    expect(screen.getByText("expose parser metrics")).toBeInTheDocument();
    expect(screen.getByText("Base patchset")).toBeInTheDocument();
    expect(screen.getAllByText("needs update").length).toBeGreaterThan(0);

    expect(screen.getByRole("heading", { name: "Add patchset" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add patchset" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Add changeset" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Dependent" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add dependent" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Sibling" }));
    expect(screen.getByRole("button", { name: "Add sibling" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Move to base" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Move and update" })).toBeInTheDocument();
  });

  it("shows clean and conflicted update results with conflict metadata", async () => {
    const api = makeApi();
    api.restack = vi.fn().mockResolvedValue({
      entries: [
        changeset("cs_child", "use parser in payment API", {
          patchsetId: "ps_child_2",
          patchsetNumber: "2",
          status: "draft"
        }),
        changeset("cs_grandchild", "update tests for API behavior", {
          conflicts: [
            {
              conflictClass: "restack",
              newBaseCommitId: "commit_new",
              oldBaseCommitId: "commit_old",
              path: "/acme/payment/conflict.go",
              remoteFingerprint: "sha256:remote"
            }
          ],
          patchsetId: "ps_grandchild_2",
          patchsetNumber: "2",
          status: "draft"
        })
      ],
      stackId: "stk_parser",
      status: "conflicts"
    });
    apiMock.current = api;

    renderRoute(<StackRestackPage />);

    expect(await screen.findByText("Affected changesets")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Update dependents" }));

    expect(await screen.findByText("Update result")).toBeInTheDocument();
    expect(screen.getAllByText("use parser in payment API").length).toBeGreaterThan(0);
    expect(screen.getAllByText("update tests for API behavior").length).toBeGreaterThan(0);
    expect(screen.getByText("Conflict details")).toBeInTheDocument();
    expect(screen.getByText("/acme/payment/conflict.go")).toBeInTheDocument();
    expect(screen.getByText("base update")).toBeInTheDocument();
    expect(screen.getByText("sha256:remote")).toBeInTheDocument();
  });

  it("shows dependency submit progress for accepted, pending, submitted, and blocked changesets", async () => {
    const api = makeApi();
    api.submitStack = vi.fn().mockResolvedValue({
      results: [
        { changesetId: "cs_root", commitId: "commit_root", status: "accepted" },
        {
          changesetId: "cs_child",
          pendingPublishId: "pending_child",
          status: "pending_publish"
        },
        { changesetId: "cs_grandchild", commitId: "commit_grandchild", status: "submitted" },
        {
          blockedReason: "required check unit failed",
          changesetId: "cs_sibling",
          status: "blocked"
        }
      ],
      stackId: "stk_parser",
      status: "partial"
    });
    apiMock.current = api;

    renderRoute(<StackSubmitPage />);

    expect(await screen.findByText("Submit order")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Submit dependencies" }));

    expect(await screen.findByText("Submit result")).toBeInTheDocument();
    expect(screen.getByText("accepted")).toBeInTheDocument();
    expect(screen.getByText("pending_publish")).toBeInTheDocument();
    expect(screen.getByText("submitted")).toBeInTheDocument();
    expect(screen.getByText("blocked")).toBeInTheDocument();
    expect(screen.getByText("required check unit failed")).toBeInTheDocument();
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
    getStack: vi.fn().mockResolvedValue(stackFixture()),
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
