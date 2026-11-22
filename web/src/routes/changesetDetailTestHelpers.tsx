import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, type RenderResult } from "@testing-library/react";
import { createElement, type ReactElement, type ReactNode } from "react";
import { vi } from "vitest";

import type { Changeset, ChangesetStack, PatchsetConflict } from "../api/types";

export const apiMock = {
  current: {} as Record<string, unknown>
};

const listeners = new Set<() => void>();
let currentSearch: Record<string, unknown> = {};

export const routerMock = {
  back: vi.fn() as ReturnType<typeof vi.fn>,
  navigate: vi.fn() as ReturnType<typeof vi.fn>,
  params: {} as Record<string, string>,
  get search() {
    return currentSearch;
  },
  set search(next: Record<string, unknown>) {
    currentSearch = next;
  },
  setSearch(next: Record<string, unknown>) {
    currentSearch = next;
    listeners.forEach((listener) => listener());
  },
  subscribe(listener: () => void) {
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }
};

export const featuresMock = { conversationsEnabled: false };

export const selectionMock = {
  current: {
    account: "nic",
    accounts: ["nic", "acme"],
    memberships: [
      { account: "nic", kind: "personal", role: "owner" },
      { account: "acme", kind: "organization", role: "admin" }
    ],
    subjectId: "user_nic"
  }
};

export function resetSelectionMock() {
  selectionMock.current = {
    account: "nic",
    accounts: ["nic", "acme"],
    memberships: [
      { account: "nic", kind: "personal", role: "owner" },
      { account: "acme", kind: "organization", role: "admin" }
    ],
    subjectId: "user_nic"
  };
}

export function resetChangesetTestHooks({
  withApi = true,
  searchReplaceOnly = false
}: {
  withApi?: boolean;
  searchReplaceOnly?: boolean;
} = {}) {
  routerMock.navigate = vi.fn((options?: { search?: unknown }) => {
    if (!options) {
      return;
    }
    const nextSearch =
      typeof options.search === "function"
        ? (options.search as (prev: Record<string, unknown>) => Record<string, unknown>)(routerMock.search)
        : (options.search as Record<string, unknown> | undefined);
    if (nextSearch !== undefined && (!searchReplaceOnly || typeof options.search === "function")) {
      routerMock.setSearch(nextSearch);
    }
  });
  routerMock.back = vi.fn();
  routerMock.params = withApi ? { id: "stk_parser" } : { id: "cs_child" };
  routerMock.search = {};
  if (withApi) {
    apiMock.current = makeChangesetApi();
  }
}

export function cleanupChangesetTestHooks() {
  featuresMock.conversationsEnabled = false;
  resetSelectionMock();
  cleanup();
  vi.clearAllMocks();
}

export function MockDiffViewer({
  fileStates,
  onFileNeeded,
  onFileRetry,
  onViewFile,
  onViewModeChange,
  viewMode
}: {
  fileStates?: {
    path: string;
    status: string;
    changeKind?: string;
    file?: { changeKind?: string };
  }[];
  onFileNeeded?(path: string): void;
  onFileRetry?(path: string): void;
  onViewFile?(file: { path: string; changeKind: string }): void;
  onViewModeChange?(mode: string): void;
  viewMode?: string;
}) {
  return (
    <div data-testid="diff-viewer">
      Diff viewer
      <span data-testid="diff-view-mode">{viewMode}</span>
      <button onClick={() => onViewModeChange?.("split")} type="button">
        Use split
      </button>
      <button onClick={() => onViewModeChange?.("unified")} type="button">
        Use unified
      </button>
      <button
        onClick={() => onViewFile?.({ changeKind: "modified", path: "acme/payment/parser.go" })}
        type="button"
      >
        View parser.go
      </button>
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
  );
}

export function renderChangesetRoute(element: ReactElement): RenderResult {
  const queryClient = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false }
    }
  });

  return render(createElement(QueryClientProvider, { client: queryClient }, element));
}

export function makeRouterLink({ children }: { children: ReactNode }) {
  return <a href="#">{children}</a>;
}

export function makeChangesetApi() {
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
    listSharedSlices: vi.fn().mockResolvedValue({ slices: [] }),
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

export function stackFixture(): ChangesetStack {
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

export function changeset(
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
