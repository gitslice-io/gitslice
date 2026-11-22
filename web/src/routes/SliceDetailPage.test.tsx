import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SliceDetailPage } from "./SliceDetailPage";
import { SliceSettingsPage } from "./SliceSettingsPage";
import {
  apiMock,
  authMock,
  cleanupSlicePagesHooks,
  expectHealthy,
  flushAsync,
  makeSlicePagesApi,
  renderSliceRoute,
  resetSlicePagesHooks,
  routerMock,
  selectionMock
} from "./slicePagesTestHelpers";

vi.mock("../lib/features", async () => {
  const { featuresMock } = await import("./slicePagesTestHelpers");
  return {
    get conversationsEnabled() {
      return featuresMock.conversationsEnabled;
    }
  };
});

vi.mock("../api/useApi", async () => {
  const { apiMock } = await import("./slicePagesTestHelpers");
  return { useApi: () => apiMock.current };
});

vi.mock("@clerk/tanstack-react-start", async () => {
  const { authMock } = await import("./slicePagesTestHelpers");
  return { useAuth: () => authMock.current };
});

vi.mock("@tanstack/react-router", async () => {
  const { makeRouterLink, routerMock } = await import("./slicePagesTestHelpers");
  return {
    Link: makeRouterLink,
    useNavigate: () => routerMock.navigate,
    useParams: () => routerMock.params,
    useRouter: () => ({ history: { back: routerMock.back, push: routerMock.push } }),
    useSearch: () => routerMock.search
  };
});

vi.mock("../state/selection", async () => {
  const { selectionMock } = await import("./slicePagesTestHelpers");
  return { useSelection: () => selectionMock.current };
});

describe("slice detail route page", () => {
  let consoleError: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    consoleError = resetSlicePagesHooks();
  });

  afterEach(() => {
    cleanupSlicePagesHooks(consoleError);
  });

  it("renders the slice detail page past the slice-loaded guard", async () => {
    // This page held the conditional-hook crash: the `useMemo` after the
    // `if (!slice) return` guard only ran once resolveSlice resolved.
    renderSliceRoute(<SliceDetailPage />);
    await waitFor(() => expect(apiMock.current.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expect((await screen.findAllByText("Files")).length).toBeGreaterThan(0);
    expectHealthy(consoleError);
  });

  it("opens the go-to-file dialog with t or its button, lists the slice's files, and opens one", async () => {
    Element.prototype.scrollIntoView = vi.fn();
    const files = ["/nic/notes/todo.md", "/nic/src/main.go"];
    // The page's api mock, which the test makes recursive-aware.
    const api = apiMock.current as unknown as { listDirectory: ReturnType<typeof vi.fn> };
    const plain = api.listDirectory as unknown as (request: { recursive?: boolean }) => Promise<unknown>;
    api.listDirectory = vi.fn((request: { recursive?: boolean }) =>
      request.recursive
        ? Promise.resolve({ recursive: true, entries: files.map((path) => ({ kind: "ENTRY_KIND_FILE", path })) })
        : plain(request)
    );
    renderSliceRoute(<SliceDetailPage />);
    await waitFor(() => expect(apiMock.current.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expect(screen.queryByRole("dialog", { name: "Find file" })).toBeNull();
    // Nothing is listed until it is wanted.
    expect((api.listDirectory.mock.calls as Array<[{ recursive?: boolean }]>).some(([request]) => request.recursive)).toBe(false);

    // Typing into a field is not a command.
    const field = document.createElement("input");
    document.body.appendChild(field);
    fireEvent.keyDown(field, { key: "t" });
    fireEvent.keyDown(window, { key: "t", ctrlKey: true });
    expect(screen.queryByRole("dialog", { name: "Find file" })).toBeNull();
    field.remove();

    fireEvent.keyDown(window, { key: "t" });
    const dialog = await screen.findByRole("dialog", { name: "Find file" });
    expect(await within(dialog).findByTitle("src/main.go")).toBeInTheDocument();
    expect(api.listDirectory).toHaveBeenCalledWith(
      expect.objectContaining({ commitId: "commit_1", path: "/nic", recursive: true, slice: { account: "nic", slice: "home" } })
    );

    fireEvent.change(within(dialog).getByRole("combobox"), { target: { value: "todo" } });
    fireEvent.keyDown(within(dialog).getByRole("combobox"), { key: "Enter" });
    expect(screen.queryByRole("dialog", { name: "Find file" })).toBeNull();
    expect(routerMock.navigate).toHaveBeenCalledWith(expect.objectContaining({ search: { path: "/nic/notes/todo.md" } }));

    // The header's button does the same.
    fireEvent.click(screen.getByRole("button", { name: "Find file" }));
    expect(await screen.findByRole("dialog", { name: "Find file" })).toBeInTheDocument();
    expectHealthy(consoleError);
  });

  it("shows whose slice it is in the breadcrumb", async () => {
    renderSliceRoute(<SliceDetailPage />);
    await waitFor(() => expect(apiMock.current.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    // Own slice: your account, then the slice.
    const own = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(within(own).getByRole("link", { name: "@nic" })).toBeInTheDocument();
    expect(own).toHaveTextContent("home");
    cleanup();

    // Someone else's slice names the owner, then the slice.
    routerMock.params = { account: "gitslice", slice: "gitslice" };
    const api = makeSlicePagesApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
      id: "slice_gitslice",
      ref: { account: "gitslice", slice: "gitslice" },
      definition: { includedPaths: ["/gitslice/gitslice"], visibility: "public" }
    });
    apiMock.current = api;
    renderSliceRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    const other = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(other).toHaveTextContent("Home");
    // The owner links to the owner's page.
    expect(within(other).getByRole("link", { name: "@gitslice" })).toBeInTheDocument();
    expect(within(other).getByText("gitslice")).toBeInTheDocument();
  });

  it("moves a renamed slice's old address to the current one", async () => {
    routerMock.params = { account: "nic", slice: "bot-channels" };
    // As the route's validateSearch (searchSchemas) hands it over: normalized.
    routerMock.search = { path: "/readme.md" };
    const api = makeSlicePagesApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
      id: "slice_nic_bot_channels",
      ref: { account: "nic", slice: "botparley" },
      formerNames: ["bot-channels"],
      definition: { includedPaths: ["/nic/botparley"], visibility: "private" }
    });
    apiMock.current = api;
    renderSliceRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expect(routerMock.navigate).toHaveBeenCalledWith(
      expect.objectContaining({
        params: { account: "nic", slice: "botparley" },
        replace: true,
        search: { path: "/readme.md" },
        to: "/slices/$account/$slice"
      })
    );
    expectHealthy(consoleError);
  });

  it("says when a slice is archived", async () => {
    const api = makeSlicePagesApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
      id: "slice_nic_home",
      ref: { account: "nic", slice: "home" },
      archived: true,
      definition: { includedPaths: ["/nic"], visibility: "private" }
    });
    apiMock.current = api;
    renderSliceRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expect(screen.getByText("This slice is archived")).toBeInTheDocument();
    expect(routerMock.navigate).not.toHaveBeenCalled();
    expectHealthy(consoleError);
  });

  describe("the root of a slice, on a phone", () => {
    function rootApi(tree: Record<string, object[]>) {
      const api = makeSlicePagesApi();
      routerMock.search = {};
      api.listDirectory = vi.fn(async (request: { path?: string }) => ({ entries: tree[request.path ?? ""] ?? [] }));
      api.readFile = vi.fn().mockResolvedValue({ data: btoa("# Hello root\n\nIntro.\n") });
      apiMock.current = api;
      return api;
    }
    const dir = { kind: "ENTRY_KIND_DIRECTORY", name: "nic", path: "/nic" };
    const file = (name: string) => ({ kind: "ENTRY_KIND_FILE", name, path: `/nic/${name}`, size: "20" });

    it("puts the Files bar above the README on a phone, so the tree is not at the end of it", async () => {
      rootApi({ "": [dir], "/nic": [file("README.md"), file("main.go")] });
      renderSliceRoute(<SliceDetailPage />);
      const heading = await screen.findByRole("heading", { name: "Hello root" }, { timeout: 5000 });
      // On a phone: the Files bar, the tree it opens, then the README, which
      // is not hidden there.
      const column = heading.closest(".max-lg\\:order-3");
      expect(column).not.toBeNull();
      expect(column?.className).not.toMatch(/(^|\s)hidden(\s|$)/);
      expect(screen.getByRole("button", { name: /^Files/ }).className).toMatch(/(^|\s)order-1(\s|$)/);
      expect(document.getElementById("slice-file-tree-panel")?.className).toMatch(/max-lg:order-2/);
    });

    it("folds the whole tree behind a Files bar under the README, until asked for", async () => {
      rootApi({ "": [dir], "/nic": [file("README.md"), file("main.go")] });
      renderSliceRoute(<SliceDetailPage />);
      await screen.findByRole("heading", { name: "Hello root" }, { timeout: 5000 });
      const bar = screen.getByRole("button", { name: /^Files/ });
      expect(bar).toHaveAttribute("aria-expanded", "false");
      expect(bar.className).toMatch(/lg:hidden/);
      expect(document.getElementById("slice-file-tree-panel")?.className).toMatch(/(^|\s)hidden(\s|$)/);
      fireEvent.click(bar);
      expect(bar).toHaveAttribute("aria-expanded", "true");
      expect(document.getElementById("slice-file-tree-panel")?.className).toMatch(/(^|\s)block(\s|$)/);
      // And folds again.
      fireEvent.click(bar);
      expect(bar).toHaveAttribute("aria-expanded", "false");
    });

    it("has no Files bar where the root has no README: the open tree is the page", async () => {
      rootApi({ "": [dir], "/nic": [file("main.go"), file("util.go")] });
      renderSliceRoute(<SliceDetailPage />);
      await screen.findByText("Content hash");
      expect(screen.queryByRole("button", { name: /^Files/ })).toBeNull();
      expect(document.getElementById("slice-file-tree-panel")?.className).toMatch(/(^|\s)block(\s|$)/);
    });

    it("leaves the tree alone when the root has no README, not repeating it as a listing", async () => {
      rootApi({ "": [dir], "/nic": [file("main.go"), file("util.go")] });
      renderSliceRoute(<SliceDetailPage />);
      const table = await screen.findByText("Content hash");
      // The folder listing is for wider screens only.
      expect(table.closest(".max-lg\\:hidden")).not.toBeNull();
    });
  });

  describe("blame on a file page", () => {
    const filePath = "/nic/app/main.go";
    function fileApi() {
      const api = makeSlicePagesApi() as ReturnType<typeof makeSlicePagesApi> & { blameFile: ReturnType<typeof vi.fn> };
      routerMock.search = { path: filePath };
      api.resolvePath = vi.fn().mockResolvedValue({ entry: { kind: "ENTRY_KIND_FILE", path: filePath, size: "30" } });
      api.readFile = vi.fn().mockResolvedValue({ data: btoa("package app\nfunc A() {}\nfunc B() {}\n") });
      api.blameFile = vi.fn().mockResolvedValue({
        commitId: "sha256:head",
        lineCount: 3,
        path: filePath,
        ranges: [
          { startLine: 1, lineCount: 1, commitId: "sha256:aaaaaaaaaaaa1111" },
          { startLine: 2, lineCount: 2, commitId: "sha256:bbbbbbbbbbbb2222" }
        ],
        commits: [
          { id: "sha256:aaaaaaaaaaaa1111", author: "alice", message: "Start", createdAt: "2026-10-01T00:00:00Z" },
          { id: "sha256:bbbbbbbbbbbb2222", author: "bob", message: "Add funcs", createdAt: "2026-10-05T00:00:00Z" }
        ]
      });
      apiMock.current = api;
      return api;
    }

    it("is turned on from the file's menu, and written to the address without a new history entry", async () => {
      fileApi();
      renderSliceRoute(<SliceDetailPage />);
      fireEvent.click(await screen.findByRole("button", { name: "File actions" }));
      fireEvent.click(screen.getByRole("menuitemcheckbox", { name: "Blame" }));
      expect(routerMock.navigate).toHaveBeenCalledWith(
        expect.objectContaining({ replace: true, resetScroll: false, search: { blame: true, path: filePath } })
      );
    });

    it("comes back as it was: blame on and the change selected, from the address alone", async () => {
      const api = fileApi();
      routerMock.search = { blame: true, blameAt: "bbbbbbbbbbbb", path: filePath };
      renderSliceRoute(<SliceDetailPage />);
      expect(await screen.findByLabelText("Blame", {}, { timeout: 5000 })).toBeInTheDocument();
      expect(api.blameFile).toHaveBeenCalled();
      await waitFor(() => expect(document.querySelector("[data-blame-highlight] [data-lines]")).toHaveAttribute("data-lines", "2-3"));
      // The menu shows it on.
      fireEvent.click(screen.getByRole("button", { name: "File actions" }));
      expect(screen.getByRole("menuitemcheckbox", { name: "Blame" })).toHaveAttribute("aria-checked", "true");
    });

    it("writes a tapped change to the address, and keeps blame on", async () => {
      fileApi();
      routerMock.search = { blame: true, path: filePath };
      renderSliceRoute(<SliceDetailPage />);
      const rows = within(await screen.findByLabelText("Blame", {}, { timeout: 5000 })).getAllByRole("row");
      fireEvent.click(rows[1]);
      expect(routerMock.navigate).toHaveBeenCalledWith(
        expect.objectContaining({ replace: true, search: { blame: true, blameAt: "bbbbbbbbbbbb", path: filePath } })
      );
    });
  });

  it("offers editing and settings only to those the slice's account allows", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    renderSliceRoute(<SliceDetailPage />);
    await waitFor(() => expect(apiMock.current.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    // Your own slice: you own the account.
    expect(screen.getByText("Settings")).toBeInTheDocument();
    expect(await screen.findByLabelText("Directory actions")).toBeInTheDocument();
    expect(screen.queryByLabelText("Create item")).not.toBeInTheDocument();
    cleanup();

    // Someone else's public slice: read only.
    routerMock.params = { account: "gitslice", slice: "gitslice" };
    const api = makeSlicePagesApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
      id: "slice_gitslice",
      ref: { account: "gitslice", slice: "gitslice" },
      definition: { includedPaths: ["/gitslice/gitslice"], visibility: "public" }
    });
    apiMock.current = api;
    renderSliceRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expect(screen.queryByText("Settings")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Directory actions")).not.toBeInTheDocument();
  });

  it("lets an organization writer edit but not configure", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "acme"],
      memberships: [...selectionMock.current.memberships, { account: "acme", kind: "organization", role: "writer" }]
    };
    routerMock.params = { account: "acme", slice: "payment" };
    const api = makeSlicePagesApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
      id: "slice_acme_payment",
      ref: { account: "acme", slice: "payment" },
      definition: { includedPaths: ["/acme/payment"], visibility: "private" }
    });
    apiMock.current = api;
    renderSliceRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expect(screen.queryByText("Settings")).not.toBeInTheDocument();
    expect(await screen.findByLabelText("Directory actions")).toBeInTheDocument();
  });

  it("lets an external slice contributor propose edits and an external slice admin manage members", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    routerMock.params = { account: "acme", slice: "payment" };
    const paymentSlice = {
      id: "slice_acme_payment",
      ref: { account: "acme", slice: "payment" },
      definition: { includedPaths: ["/acme/payment"], visibility: "private" }
    };
    const api = makeSlicePagesApi();
    api.resolveSlice = vi.fn().mockResolvedValue(paymentSlice);
    api.listDirectory = vi.fn().mockResolvedValue({
      entries: [{ kind: "ENTRY_KIND_DIRECTORY", name: "payment", path: "/acme/payment" }]
    });
    api.resolvePath = vi.fn().mockResolvedValue({
      entry: { kind: "ENTRY_KIND_DIRECTORY", path: "/acme/payment" }
    });
    api.listSharedSlices = vi.fn().mockResolvedValue({
      slices: [{ role: "contributor", slice: paymentSlice }]
    });
    apiMock.current = api;

    renderSliceRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.listSharedSlices).toHaveBeenCalled());
    await flushAsync();
    // Contributors can propose changesets (Directory actions) without account membership,
    // and cannot configure slice settings.
    expect(await screen.findByLabelText("Directory actions")).toBeInTheDocument();
    expect(screen.queryByText("Settings")).not.toBeInTheDocument();
    cleanup();

    // Shared as slice admin (direct or @group): sees Settings + People panel,
    // without account-owner-only Secrets/GitMirror/Definition forms.
    const adminApi = makeSlicePagesApi();
    adminApi.resolveSlice = vi.fn().mockResolvedValue(paymentSlice);
    adminApi.listSharedSlices = vi.fn().mockResolvedValue({
      slices: [{ role: "admin", slice: paymentSlice }]
    });
    adminApi.listSliceMembers = vi.fn().mockResolvedValue({ members: [], groupMembers: [] });
    apiMock.current = adminApi;

    renderSliceRoute(<SliceDetailPage />);
    expect(await screen.findByText("Settings")).toBeInTheDocument();
    cleanup();

    renderSliceRoute(<SliceSettingsPage />);
    expect(await screen.findByRole("heading", { name: "People" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save definition" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Secrets" })).not.toBeInTheDocument();
    expectHealthy(consoleError);
  });

  it("creates a changeset and redirects after a folder operation", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    const api = makeSlicePagesApi();
    api.createChangeset = vi
      .fn()
      .mockResolvedValue({ id: "cs_abcdef123456", currentPatchsetId: "ps_1" });
    api.updateChangeset = vi.fn().mockResolvedValue({ id: "ps_2" });
    apiMock.current = api;

    renderSliceRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.resolveSlice).toHaveBeenCalled());
    await flushAsync();

    // Open the single "Directory actions" menu and start a new folder (no duplicate dropdown).
    // The menu appears once the slice permissions load; wait for it.
    expect(await screen.findAllByLabelText("Directory actions")).toHaveLength(1);
    expect(screen.queryByLabelText("Create item")).not.toBeInTheDocument();
    fireEvent.click(await screen.findByLabelText("Directory actions"));
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
    expectHealthy(consoleError);
  });
});
