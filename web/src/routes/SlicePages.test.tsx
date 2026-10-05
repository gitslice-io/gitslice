import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RpcError } from "../api/client";
import { AccountPage } from "./AccountPage";
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

const selectionMock = vi.hoisted(() => {
  const personal = { account: "nic", kind: "personal", role: "owner" };
  return {
    current: {
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
    }
  };
});

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

  it("shows whose slice it is in the breadcrumb", async () => {
    renderRoute(<SliceDetailPage />);
    await waitFor(() => expect(apiMock.current.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    // Own slice: your account, then the slice.
    const own = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(within(own).getByRole("link", { name: "@nic" })).toBeInTheDocument();
    expect(own).toHaveTextContent("home");
    cleanup();

    // Someone else's slice names the owner, then the slice.
    routerMock.params = { account: "gitslice", slice: "gitslice" };
    const api = makeApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
      id: "slice_gitslice",
      ref: { account: "gitslice", slice: "gitslice" },
      definition: { includedPaths: ["/gitslice/gitslice"], visibility: "public" }
    });
    apiMock.current = api;
    renderRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    const other = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(other).toHaveTextContent("Home");
    // The owner links to the owner's page.
    expect(within(other).getByRole("link", { name: "@gitslice" })).toBeInTheDocument();
    expect(within(other).getByText("gitslice")).toBeInTheDocument();
  });

  it("lists an account's slices on its page", async () => {
    routerMock.params = { account: "gitslice" };
    const api = makeApi();
    api.listSlices = vi.fn().mockResolvedValue({
      slices: [
        { id: "s1", ref: { account: "gitslice", slice: "gitslice" }, definition: { includedPaths: ["/gitslice/gitslice"], visibility: "public" } },
        { id: "s2", ref: { account: "gitslice", slice: "docs" }, definition: { includedPaths: ["/gitslice/docs"], visibility: "public" } }
      ]
    });
    apiMock.current = api;
    renderRoute(<AccountPage />);

    expect(await screen.findByText("docs")).toBeInTheDocument();
    expect(api.listSlices).toHaveBeenCalledWith(expect.objectContaining({ account: "gitslice" }));
    expect(screen.getByRole("heading", { name: "gitslice" })).toBeInTheDocument();
    expect(screen.getByText("Public slices owned by gitslice.")).toBeInTheDocument();
    expectHealthy();
  });

  it("pages through a long list of slices", async () => {
    routerMock.params = { account: "big" };
    const api = makeApi();
    api.listSlices = vi
      .fn()
      .mockResolvedValueOnce({
        slices: [{ id: "a", ref: { account: "big", slice: "alpha" }, definition: { visibility: "public" } }],
        nextCursor: "alpha"
      })
      .mockResolvedValueOnce({
        slices: [{ id: "b", ref: { account: "big", slice: "bravo" }, definition: { visibility: "public" } }]
      });
    apiMock.current = api;
    renderRoute(<AccountPage />);

    expect(await screen.findByText("alpha")).toBeInTheDocument();
    expect(screen.queryByText("bravo")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(await screen.findByText("bravo")).toBeInTheDocument();
    expect(api.listSlices).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: "alpha" }));
    expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
  });

  it("does not retry an answer that will not change", async () => {
    routerMock.params = { account: "gitslice" };
    const api = makeApi();
    api.listSlices = vi.fn().mockRejectedValue(new RpcError(401, { code: "unauthenticated", message: "missing subject" }));
    apiMock.current = api;
    renderRoute(<AccountPage />);

    expect(await screen.findByText("Could not load slices")).toBeInTheDocument();
    expect(api.listSlices).toHaveBeenCalledTimes(1);
  });

  it("says when the account does not exist", async () => {
    routerMock.params = { account: "ghost" };
    const api = makeApi();
    api.listSlices = vi.fn().mockRejectedValue(new RpcError(404, { code: 5, message: "not found" }));
    apiMock.current = api;
    renderRoute(<AccountPage />);

    expect(await screen.findByText("No such account")).toBeInTheDocument();
    expect(api.listSlices).toHaveBeenCalledTimes(1);
  });

  it("tells a member of an organization that its page shows everything to them", async () => {
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "acme"],
      memberships: [...selectionMock.current.memberships, { account: "acme", kind: "organization", role: "writer" }]
    };
    routerMock.params = { account: "acme" };
    const api = makeApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [] });
    apiMock.current = api;
    renderRoute(<AccountPage />);

    expect(await screen.findByText(/Slices of acme, which you belong to/)).toBeInTheDocument();
    selectionMock.current = { ...selectionMock.current, accounts: ["nic"], memberships: selectionMock.current.memberships.slice(0, 1) };
  });

  it("lets an organization owner manage its people", async () => {
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "gitslice"],
      memberships: [...selectionMock.current.memberships, { account: "gitslice", kind: "organization", role: "owner" }]
    };
    routerMock.params = { account: "gitslice" };
    const api = makeApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [], accountKind: "organization" });
    api.listAccountMembers = vi.fn().mockResolvedValue({
      account: "gitslice",
      kind: "organization",
      members: [
        { username: "nic", subjectId: "user_1", role: "owner" },
        { username: "gitslice-mirror", subjectId: "agent_2", role: "writer" }
      ]
    });
    api.setAccountMember = vi.fn().mockResolvedValue({});
    api.removeAccountMember = vi.fn().mockResolvedValue({});
    apiMock.current = api;
    renderRoute(<AccountPage />);

    const people = await screen.findByRole("region", { name: "People" });
    expect(await within(people).findByText("gitslice-mirror")).toBeInTheDocument();
    expect(screen.getByText(/Organization · you are an owner/)).toBeInTheDocument();

    fireEvent.change(within(people).getByLabelText("Role of gitslice-mirror"), { target: { value: "reader" } });
    await waitFor(() =>
      expect(api.setAccountMember).toHaveBeenCalledWith({ account: "gitslice", role: "reader", username: "gitslice-mirror" })
    );

    fireEvent.click(within(people).getAllByRole("button", { name: "Remove" })[1]);
    await waitFor(() => expect(api.removeAccountMember).toHaveBeenCalledWith({ account: "gitslice", username: "gitslice-mirror" }));

    fireEvent.change(within(people).getByPlaceholderText("username"), { target: { value: "@alice" } });
    fireEvent.click(within(people).getByRole("button", { name: "Add" }));
    await waitFor(() =>
      expect(api.setAccountMember).toHaveBeenLastCalledWith({ account: "gitslice", role: "writer", username: "alice" })
    );
    selectionMock.current = { ...selectionMock.current, accounts: ["nic"], memberships: selectionMock.current.memberships.slice(0, 1) };
  });

  it("shows a reader the people but not the controls", async () => {
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "acme"],
      memberships: [...selectionMock.current.memberships, { account: "acme", kind: "organization", role: "reader" }]
    };
    routerMock.params = { account: "acme" };
    const api = makeApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [], accountKind: "organization" });
    api.listAccountMembers = vi.fn().mockResolvedValue({ members: [{ username: "boss", role: "owner" }] });
    apiMock.current = api;
    renderRoute(<AccountPage />);

    const people = await screen.findByRole("region", { name: "People" });
    expect(await within(people).findByText("boss")).toBeInTheDocument();
    expect(within(people).queryByRole("button", { name: "Remove" })).not.toBeInTheDocument();
    expect(within(people).queryByPlaceholderText("username")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New slice" })).not.toBeInTheDocument();
    selectionMock.current = { ...selectionMock.current, accounts: ["nic"], memberships: selectionMock.current.memberships.slice(0, 1) };
  });

  it("says when an account has no public slices", async () => {
    routerMock.params = { account: "quiet" };
    const api = makeApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [] });
    apiMock.current = api;
    renderRoute(<AccountPage />);

    expect(await screen.findByText("No public slices")).toBeInTheDocument();
  });

  it("offers editing and settings only to those the slice's account allows", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    renderRoute(<SliceDetailPage />);
    await waitFor(() => expect(apiMock.current.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    // Your own slice: you own the account.
    expect(screen.getByText("Settings")).toBeInTheDocument();
    expect((await screen.findAllByLabelText("Create item")).length).toBeGreaterThan(0);
    cleanup();

    // Someone else's public slice: read only.
    routerMock.params = { account: "gitslice", slice: "gitslice" };
    const api = makeApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
      id: "slice_gitslice",
      ref: { account: "gitslice", slice: "gitslice" },
      definition: { includedPaths: ["/gitslice/gitslice"], visibility: "public" }
    });
    apiMock.current = api;
    renderRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expect(screen.queryByText("Settings")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Create item")).not.toBeInTheDocument();
  });

  it("lets an organization writer edit but not configure", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "acme"],
      memberships: [...selectionMock.current.memberships, { account: "acme", kind: "organization", role: "writer" }]
    };
    routerMock.params = { account: "acme", slice: "payment" };
    const api = makeApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
      id: "slice_acme_payment",
      ref: { account: "acme", slice: "payment" },
      definition: { includedPaths: ["/acme/payment"], visibility: "private" }
    });
    apiMock.current = api;
    renderRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expect(screen.queryByText("Settings")).not.toBeInTheDocument();
    expect((await screen.findAllByLabelText("Create item")).length).toBeGreaterThan(0);
    selectionMock.current = { ...selectionMock.current, accounts: ["nic"], memberships: selectionMock.current.memberships.slice(0, 1) };
  });

  it("creates a slice in an organization the viewer administers", async () => {
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "gitslice", "acme"],
      memberships: [
        ...selectionMock.current.memberships,
        { account: "gitslice", kind: "organization", role: "owner" },
        { account: "acme", kind: "organization", role: "reader" }
      ]
    };
    routerMock.search = { account: "gitslice" };
    renderRoute(<SliceCreatePage />);
    await flushAsync();
    const select = screen.getByRole("combobox");
    expect(select).toHaveValue("gitslice");
    // Only accounts you own or administer are offered.
    expect(within(select).getAllByRole("option").map((o) => o.getAttribute("value"))).toEqual(["nic", "gitslice"]);
    selectionMock.current = { ...selectionMock.current, accounts: ["nic"], memberships: selectionMock.current.memberships.slice(0, 1) };
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
    listAccountMembers: vi.fn().mockResolvedValue({ members: [] }),
    setAccountMember: vi.fn().mockResolvedValue({}),
    removeAccountMember: vi.fn().mockResolvedValue({}),
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
