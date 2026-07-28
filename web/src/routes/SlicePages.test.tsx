import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { HomePage } from "./HomePage";
import { SliceCreatePage } from "./SliceCreatePage";
import { SliceDetailPage } from "./SliceDetailPage";
import { SliceSettingsPage } from "./SliceSettingsPage";

const apiMock = vi.hoisted(() => ({
  current: {} as Record<string, unknown>
}));

const routerMock = vi.hoisted(() => ({
  back: vi.fn(),
  push: vi.fn(),
  navigate: vi.fn(),
  params: {} as Record<string, string>,
  search: {} as Record<string, unknown>
}));

const authMock = vi.hoisted(() => ({
  current: { isLoaded: true, isSignedIn: false }
}));

const selectionMock = vi.hoisted(() => ({
  current: {
    account: "nic",
    accounts: ["nic"],
    error: null as Error | null,
    isLoading: false,
    needsUsername: false,
    subjectId: "user_1"
  }
}));

vi.mock("../api/useApi", () => ({
  useApi: () => apiMock.current
}));

vi.mock("@clerk/tanstack-react-start", () => ({
  useAuth: () => authMock.current
}));

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>,
  useNavigate: () => routerMock.navigate,
  useParams: () => routerMock.params,
  useRouter: () => ({ history: { back: routerMock.back, push: routerMock.push } }),
  useSearch: () => routerMock.search
}));

vi.mock("../state/selection", () => ({
  useSelection: () => selectionMock.current
}));

// Smoke test: render the slice flow pages past their data guards and fail on
// any render crash (e.g. conditional hooks -> "Rendered more hooks") or React
// error log (e.g. "Maximum update depth" render loops). This guards the class
// of regression that a green TypeScript build does not catch.
describe("slice route pages (render smoke)", () => {
  let consoleError: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    routerMock.navigate = vi.fn();
    routerMock.back = vi.fn();
    routerMock.params = { account: "nic", slice: "home" };
    routerMock.search = {};
    authMock.current = { isLoaded: true, isSignedIn: false };
    apiMock.current = makeApi();
    consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    cleanup();
    consoleError.mockRestore();
    vi.clearAllMocks();
  });

  // A render crash (e.g. conditional hooks) surfaces as a thrown error or, when
  // an error boundary catches it, as "Something went wrong"; a render loop logs
  // "Maximum update depth". Assert none occurred after data has loaded.
  function expectHealthy() {
    expect(screen.queryByText(/Something went wrong/i)).toBeNull();
    const offending = consoleError.mock.calls
      .map((args: unknown[]) => String(args[0]))
      .filter((message: string) =>
        /Maximum update depth|Rendered more hooks|hooks than during/.test(message)
      );
    expect(offending).toEqual([]);
  }

  it("renders the slices list", async () => {
    renderRoute(<HomePage />);
    await waitFor(() => expect(apiMock.current.listSlices).toHaveBeenCalled());
    await flushAsync();
    expectHealthy();
  });

  it("renders the slice detail page past the slice-loaded guard", async () => {
    // This page held the conditional-hook crash: the `useMemo` after the
    // `if (!slice) return` guard only ran once resolveSlice resolved.
    renderRoute(<SliceDetailPage />);
    await waitFor(() => expect(apiMock.current.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expect((await screen.findAllByText("Files")).length).toBeGreaterThan(0);
    expectHealthy();
  });

  it("renders the slice settings page", async () => {
    renderRoute(<SliceSettingsPage />);
    await waitFor(() => expect(apiMock.current.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expectHealthy();
  });

  it("updates the slice CI daemon from settings", async () => {
    const api = makeApi();
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
      ciDaemonId: "daemon_1"
    };
    api.resolveSlice = vi.fn().mockResolvedValue(slice);
    api.listDaemons = vi.fn().mockResolvedValue({
      daemons: [
        {
          id: "daemon_1",
          name: "runner-one",
          runtime: "codex",
          status: "online",
          version: "1.0.0"
        },
        {
          id: "daemon_2",
          name: "runner-two",
          runtime: "codex",
          status: "online",
          version: "1.0.1"
        },
        {
          id: "daemon_3",
          name: "offline-runner",
          runtime: "codex",
          status: "offline",
          version: "1.0.0"
        }
      ]
    });
    api.setSliceCIDaemon = vi.fn().mockResolvedValue({
      ...slice,
      ciDaemonId: "daemon_2"
    });
    apiMock.current = api;

    renderRoute(<SliceSettingsPage />);

    expect(await screen.findByText("daemon_1")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("CI daemon"), {
      target: { value: "daemon_2" }
    });

    await waitFor(() =>
      expect(api.setSliceCIDaemon).toHaveBeenCalledWith({
        daemonId: "daemon_2",
        slice: { account: "nic", slice: "home" }
      })
    );
  });

  it("creates a changeset and redirects after a folder operation", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    const api = makeApi();
    api.createChangeset = vi
      .fn()
      .mockResolvedValue({ id: "cs_abcdef123456", currentPatchsetId: "ps_1" });
    api.updateChangeset = vi.fn().mockResolvedValue({ id: "ps_2" });
    apiMock.current = api;

    renderRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.resolveSlice).toHaveBeenCalled());
    await flushAsync();

    // Open the directory "Create item" menu and start a new folder.
    fireEvent.click(await screen.findByLabelText("Create item"));
    fireEvent.click(await screen.findByRole("menuitem", { name: "New folder" }));
    fireEvent.change(screen.getByPlaceholderText(/docs or/i), {
      target: { value: "images" }
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    // The operation creates the draft changeset and stages the mkdir edit...
    await waitFor(() => expect(api.createChangeset).toHaveBeenCalled());
    await waitFor(() => expect(api.updateChangeset).toHaveBeenCalled());

    // ...then redirects to the changeset it landed in.
    await waitFor(() =>
      expect(routerMock.navigate).toHaveBeenCalledWith(
        expect.objectContaining({ to: "/cs/$id" })
      )
    );
    expectHealthy();
  });

  it("renders the new slice page", async () => {
    renderRoute(<SliceCreatePage />);
    await flushAsync();
    expectHealthy();
  });
});

async function flushAsync() {
  // Let queued queries/effects settle so any post-load render loop or crash has
  // a chance to fire before we assert health.
  await new Promise((resolve) => setTimeout(resolve, 50));
}

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
  return {
    resolveSlice: vi.fn().mockResolvedValue(slice),
    getSlice: vi.fn().mockResolvedValue(slice),
    listSlices: vi.fn().mockResolvedValue({ slices: [slice], nextCursor: "" }),
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
}
