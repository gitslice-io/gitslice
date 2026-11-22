import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, type RenderResult } from "@testing-library/react";
import { createElement, type ReactElement, type ReactNode } from "react";
import { expect, vi } from "vitest";
import { marked } from "marked";

import { RpcError } from "../api/rpcError";
import { decodeBase64File } from "../components/source/sourceUtils";
import { markRepoImages, projectRoot } from "../lib/repoLinks";

export const apiMock = {
  current: {} as Record<string, unknown>
};

export const routerMock = {
  back: vi.fn(),
  push: vi.fn(),
  navigate: vi.fn(),
  params: {} as Record<string, string>,
  search: {} as Record<string, unknown>
};

export const authMock = {
  current: { isLoaded: true, isSignedIn: false }
};

function defaultSelection() {
  const personal = { account: "nic", kind: "personal", role: "owner" };
  return {
    account: "nic",
    accounts: ["nic"],
    activeAccount: "nic",
    activeMembership: personal as { account: string; kind: string; role: string } | undefined,
    error: null as Error | null,
    isLoading: false,
    memberships: [personal] as { account: string; kind: string; role: string }[],
    needsUsername: false,
    setActiveAccount: (() => undefined) as (account: string) => void,
    subjectId: "user_1"
  };
}

export const selectionMock = {
  current: defaultSelection()
};

export const featuresMock = { conversationsEnabled: false };

export function makeRouterLink({ children }: { children: ReactNode }) {
  return <a href="#">{children}</a>;
}

export function expectHealthy(consoleError: { mock: { calls: unknown[][] } }) {
  expect(screen.queryByText(/Something went wrong/i)).toBeNull();
  const offending = consoleError.mock.calls
    .map((args: unknown[]) => String(args[0]))
    .filter((message: string) =>
      /Maximum update depth|Rendered more hooks|hooks than during/.test(message)
    );
  expect(offending).toEqual([]);
}

export function resetSlicePagesHooks() {
  routerMock.navigate = vi.fn();
  routerMock.back = vi.fn();
  routerMock.params = { account: "nic", slice: "home" };
  routerMock.search = {};
  authMock.current = { isLoaded: true, isSignedIn: false };
  selectionMock.current = defaultSelection();
  apiMock.current = makeSlicePagesApi();
  return vi.spyOn(console, "error").mockImplementation(() => {});
}

export function cleanupSlicePagesHooks(consoleError: { mockRestore(): void }) {
  featuresMock.conversationsEnabled = false;
  selectionMock.current = defaultSelection();
  cleanup();
  consoleError.mockRestore();
  vi.clearAllMocks();
}

export async function flushAsync() {
  // Let queued queries/effects settle so any post-load render loop or crash has
  // a chance to fire before we assert health.
  await new Promise((resolve) => setTimeout(resolve, 50));
}

export function renderSliceRoute(element: ReactElement): RenderResult {
  const queryClient = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false }
    }
  });
  return render(createElement(QueryClientProvider, { client: queryClient }, element));
}

export function makeSlicePagesApi() {
  const slice = {
    id: "slice_nic_home",
    ref: { account: "nic", slice: "home" },
    definition: {
      sliceId: "slice_nic_home",
      version: "2",
      includedPaths: ["/nic"],
      visibility: "public",
      requiredApprovals: 0,
      requiredChecks: []
    },
    definitionHash: "sha256:abc",
    ciDaemonId: ""
  };
  const api = {
    resolveSlice: vi.fn().mockResolvedValue(slice),
    getSlice: vi.fn().mockResolvedValue(slice),
    listSlices: vi.fn().mockResolvedValue({ slices: [slice], nextCursor: "" }),
    listSharedSlices: vi.fn().mockResolvedValue({ slices: [] }),
    listSliceMembers: vi.fn().mockResolvedValue({ members: [], groupMembers: [] }),
    listSliceSecrets: vi.fn().mockResolvedValue({ secrets: [] }),
    getSliceGitMirror: vi.fn().mockRejectedValue(new RpcError(404, { code: "not_found" })),
    listAccountMembers: vi.fn().mockResolvedValue({ members: [] }),
    setAccountMember: vi.fn().mockResolvedValue({}),
    removeAccountMember: vi.fn().mockResolvedValue({}),
    listMyInvitations: vi.fn().mockResolvedValue({ invitations: [] }),
    listOwnedAgents: vi.fn().mockResolvedValue({ agents: [] }),
    listPendingClaims: vi.fn().mockResolvedValue({ claims: [] }),
    respondToInvitation: vi.fn().mockResolvedValue({}),
    listAccountInvitations: vi.fn().mockResolvedValue({ invitations: [] }),
    inviteAccountMember: vi.fn().mockResolvedValue({}),
    cancelAccountInvitation: vi.fn().mockResolvedValue({}),
    getAccountProfile: vi.fn().mockImplementation(({ account }: { account: string }) =>
      Promise.resolve({ account, kind: account === "nic" ? "personal" : "organization" })
    ),
    updateAccountProfile: vi.fn(),
    setAccountAvatar: vi.fn(),
    createOrganization: vi.fn(),
    getRef: vi.fn().mockResolvedValue({ name: "refs/global/main", commitId: "commit_1" }),
    listDirectory: vi.fn().mockResolvedValue({
      entries: [
        { kind: "ENTRY_KIND_DIRECTORY", name: "nic", path: "/nic" }
      ]
    }),
    resolvePath: vi.fn().mockResolvedValue({
      entry: { kind: "ENTRY_KIND_DIRECTORY", path: "/nic" }
    }),
    readFile: vi.fn().mockResolvedValue({ data: btoa("hi\n") }),
    listConversations: vi.fn().mockResolvedValue({ conversations: [] }),
    listDaemons: vi.fn().mockResolvedValue({ daemons: [] }),
    listCommits: vi.fn().mockResolvedValue({ commits: [] }),
    listChangesets: vi.fn().mockResolvedValue({ changesets: [] }),
    setSliceCIDaemon: vi.fn().mockResolvedValue(slice),
    updateSliceDefinition: vi.fn().mockResolvedValue(slice.definition),
    createSlice: vi.fn().mockResolvedValue(slice),
    uploadBlob: vi.fn(),
    updateChangeset: vi.fn(),
    createChangeset: vi.fn()
  };
  // The server renders a README (RenderFile); here it is rendered from what
  // readFile gives, as the server does: Markdown, then repository images
  // marked for the viewer to load.
  return Object.assign(api, {
    renderFile: vi.fn(async (request: { commitId?: string; path?: string; slice?: unknown }) => {
      const read = await (api as unknown as { readFile(r: unknown): Promise<{ data?: string }> }).readFile(request);
      const path = request.path ?? "";
      const html = marked(decodeBase64File(read.data), { async: false }) as string;
      return { html: markRepoImages(html, path.slice(0, path.lastIndexOf("/")), projectRoot(["/nic"], path)) };
    })
  });
}
