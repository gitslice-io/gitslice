import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement, ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RpcError } from "../api/client";
import { AccountPage } from "./AccountPage";
import { HomePage } from "./HomePage";
import { NewOrganizationPage } from "./NewOrganizationPage";
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

  it("scopes Home to the active organization and offers invitations", async () => {
    authMock.current = { isLoaded: true, isSignedIn: true };
    const setActiveAccount = vi.fn();
    const acme = { account: "acme", kind: "organization", role: "writer" };
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "acme"],
      activeAccount: "acme",
      activeMembership: acme,
      memberships: [...selectionMock.current.memberships, acme],
      setActiveAccount
    };
    const api = makeApi();
    api.listConversations = vi.fn().mockResolvedValue({
      conversations: [
        { id: "c1", title: "Personal chat", slice: { account: "nic", slice: "home" }, createdAt: "2026-10-01T00:00:00Z" },
        { id: "c2", title: "Team chat", slice: { account: "acme", slice: "payment" }, createdAt: "2026-10-02T00:00:00Z" }
      ]
    });
    api.listMyInvitations = vi.fn().mockResolvedValue({ invitations: [{ account: "labs", role: "reader", invitedBy: "boss" }] });
    apiMock.current = api;
    renderRoute(<HomePage />);

    expect(await screen.findByText("Team chat")).toBeInTheDocument();
    expect(screen.queryByText("Personal chat")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "acme · Organization" })).toBeInTheDocument();
    expect(api.listPendingClaims).not.toHaveBeenCalled();
    expect(api.listSlices).toHaveBeenCalledWith(expect.objectContaining({ account: "acme" }));

    const invitations = await screen.findByRole("region", { name: "You are invited to an organization" });
    fireEvent.click(within(invitations).getByRole("button", { name: "Accept" }));
    await waitFor(() => expect(api.respondToInvitation).toHaveBeenCalledWith({ account: "labs", accept: true }));
    await waitFor(() => expect(setActiveAccount).toHaveBeenCalledWith("labs"));

    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic"],
      activeAccount: "nic",
      activeMembership: selectionMock.current.memberships[0],
      memberships: selectionMock.current.memberships.slice(0, 1),
      setActiveAccount: () => undefined
    };
  });

  it("creates an organization and switches to it", async () => {
    const setActiveAccount = vi.fn();
    selectionMock.current = { ...selectionMock.current, setActiveAccount };
    const api = makeApi();
    api.createOrganization = vi.fn().mockResolvedValue({ account: "acme-labs" });
    apiMock.current = api;
    renderRoute(<NewOrganizationPage />);

    const input = screen.getByPlaceholderText("acme-labs");
    fireEvent.change(input, { target: { value: "ab" } });
    fireEvent.click(screen.getByRole("button", { name: "Create organization" }));
    expect(await screen.findByText(/Use 4 to 63 letters/)).toBeInTheDocument();
    expect(api.createOrganization).not.toHaveBeenCalled();

    fireEvent.change(input, { target: { value: "Acme_Labs" } });
    fireEvent.click(screen.getByRole("button", { name: "Create organization" }));
    await waitFor(() => expect(api.createOrganization).toHaveBeenCalledWith({ slug: "acme-labs" }));
    await waitFor(() => expect(setActiveAccount).toHaveBeenCalledWith("acme-labs"));
    await waitFor(() =>
      expect(routerMock.navigate).toHaveBeenCalledWith({ params: { account: "acme-labs" }, to: "/accounts/$account" })
    );
    selectionMock.current = { ...selectionMock.current, setActiveAccount: () => undefined };
  });

  it("shows an account's profile and lets its admins edit it", async () => {
    selectionMock.current = {
      ...selectionMock.current,
      accounts: ["nic", "gitslice"],
      memberships: [...selectionMock.current.memberships, { account: "gitslice", kind: "organization", role: "owner" }]
    };
    routerMock.params = { account: "gitslice" };
    const api = makeApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [], accountKind: "organization" });
    api.getAccountProfile = vi.fn().mockResolvedValue({
      account: "gitslice",
      kind: "organization",
      displayName: "Gitslice",
      description: "Slices of a source graph.",
      website: "https://gitslice.io"
    });
    api.updateAccountProfile = vi.fn().mockResolvedValue({
      account: "gitslice",
      kind: "organization",
      displayName: "Gitslice HQ",
      description: "Slices of a source graph.",
      website: "https://gitslice.io"
    });
    apiMock.current = api;
    renderRoute(<AccountPage />);

    expect(await screen.findByRole("heading", { name: "Gitslice" })).toBeInTheDocument();
    expect(screen.getByText("Slices of a source graph.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "gitslice.io" })).toHaveAttribute("rel", "noopener noreferrer nofollow ugc");

    fireEvent.click(screen.getByRole("button", { name: "Edit profile" }));
    const form = screen.getByRole("form", { name: "Edit profile" });
    fireEvent.change(within(form).getByLabelText("Display name"), { target: { value: "Gitslice HQ" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save profile" }));
    await waitFor(() =>
      expect(api.updateAccountProfile).toHaveBeenCalledWith({
        account: "gitslice",
        description: "Slices of a source graph.",
        displayName: "Gitslice HQ",
        website: "https://gitslice.io"
      })
    );
    expect(await screen.findByRole("heading", { name: "Gitslice HQ" })).toBeInTheDocument();
    selectionMock.current = { ...selectionMock.current, accounts: ["nic"], memberships: selectionMock.current.memberships.slice(0, 1) };
  });

  it("lists your organizations and agents on your own page", async () => {
    const original = selectionMock.current;
    authMock.current = { isLoaded: true, isSignedIn: true };
    selectionMock.current = {
      ...original,
      accounts: ["nic", "gitslice", "heibot"],
      memberships: [
        ...original.memberships,
        { account: "gitslice", kind: "organization", role: "owner" },
        { account: "heibot", kind: "agent", role: "owner" }
      ]
    };
    routerMock.params = { account: "nic" };
    const api = makeApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [], accountKind: "personal" });
    api.getAccountProfile = vi.fn().mockResolvedValue({ account: "nic", kind: "personal" });
    api.listOwnedAgents = vi.fn().mockResolvedValue({
      agents: [{ account: "heibot", agentDisplayName: "hei", agentSubjectId: "agent_1", claimedAt: "2026-09-30T02:01:48Z" }]
    });
    apiMock.current = api;
    try {
      renderRoute(<AccountPage />);

      const orgs = await screen.findByRole("region", { name: "Organizations" });
      expect(within(orgs).getByRole("link", { name: /gitslice/ })).toBeInTheDocument();
      expect(within(orgs).getByText("owner")).toBeInTheDocument();
      expect(within(orgs).getByRole("link", { name: "New organization" })).toBeInTheDocument();
      // The agent cards moved here from Home.
      const agents = await screen.findByRole("region", { name: "Your agents" });
      expect(within(agents).getByText("hei")).toBeInTheDocument();
    } finally {
      selectionMock.current = original;
      authMock.current = { isLoaded: true, isSignedIn: false };
    }
  });

  it("does not offer to edit someone else's profile", async () => {
    routerMock.params = { account: "stranger" };
    const api = makeApi();
    api.listSlices = vi.fn().mockResolvedValue({ slices: [], accountKind: "personal" });
    api.getAccountProfile = vi.fn().mockResolvedValue({ account: "stranger", kind: "personal" });
    apiMock.current = api;
    renderRoute(<AccountPage />);

    expect(await screen.findByText(/Personal/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit profile" })).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "People" })).not.toBeInTheDocument();
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

  it("moves a renamed slice's old address to the current one", async () => {
    routerMock.params = { account: "nic", slice: "bot-channels" };
    routerMock.search = { path: "readme.md" };
    const api = makeApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
      id: "slice_nic_bot_channels",
      ref: { account: "nic", slice: "botparley" },
      formerNames: ["bot-channels"],
      definition: { includedPaths: ["/nic/botparley"], visibility: "private" }
    });
    apiMock.current = api;
    renderRoute(<SliceDetailPage />);
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
    expectHealthy();
  });

  it("says when a slice is archived", async () => {
    const api = makeApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
      id: "slice_nic_home",
      ref: { account: "nic", slice: "home" },
      archived: true,
      definition: { includedPaths: ["/nic"], visibility: "private" }
    });
    apiMock.current = api;
    renderRoute(<SliceDetailPage />);
    await waitFor(() => expect(api.resolveSlice).toHaveBeenCalled());
    await flushAsync();
    expect(screen.getByText("This slice is archived")).toBeInTheDocument();
    expect(routerMock.navigate).not.toHaveBeenCalled();
    expectHealthy();
  });

  describe("the root of a slice, on a phone", () => {
    function rootApi(tree: Record<string, object[]>) {
      const api = makeApi();
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
      renderRoute(<SliceDetailPage />);
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
      renderRoute(<SliceDetailPage />);
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
      renderRoute(<SliceDetailPage />);
      await screen.findByText("Content hash");
      expect(screen.queryByRole("button", { name: /^Files/ })).toBeNull();
      expect(document.getElementById("slice-file-tree-panel")?.className).toMatch(/(^|\s)block(\s|$)/);
    });

    it("leaves the tree alone when the root has no README, not repeating it as a listing", async () => {
      rootApi({ "": [dir], "/nic": [file("main.go"), file("util.go")] });
      renderRoute(<SliceDetailPage />);
      const table = await screen.findByText("Content hash");
      // The folder listing is for wider screens only.
      expect(table.closest(".max-lg\\:hidden")).not.toBeNull();
    });
  });

  describe("blame on a file page", () => {
    const filePath = "/nic/app/main.go";
    function fileApi() {
      const api = makeApi() as ReturnType<typeof makeApi> & { blameFile: ReturnType<typeof vi.fn> };
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
      renderRoute(<SliceDetailPage />);
      fireEvent.click(await screen.findByRole("button", { name: "File actions" }));
      fireEvent.click(screen.getByRole("menuitemcheckbox", { name: "Blame" }));
      expect(routerMock.navigate).toHaveBeenCalledWith(
        expect.objectContaining({ replace: true, resetScroll: false, search: { blame: true, path: filePath } })
      );
    });

    it("comes back as it was: blame on and the change selected, from the address alone", async () => {
      const api = fileApi();
      routerMock.search = { blame: true, blameAt: "bbbbbbbbbbbb", path: filePath };
      renderRoute(<SliceDetailPage />);
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
      renderRoute(<SliceDetailPage />);
      const rows = within(await screen.findByLabelText("Blame", {}, { timeout: 5000 })).getAllByRole("row");
      fireEvent.click(rows[1]);
      expect(routerMock.navigate).toHaveBeenCalledWith(
        expect.objectContaining({ replace: true, search: { blame: true, blameAt: "bbbbbbbbbbbb", path: filePath } })
      );
    });
  });

  describe("a folder with a README", () => {
    const readmeText = "# Hello app\n\nStart with the [guide](docs/guide.md) or [the site](https://example.com).\n\n![logo](logo.png)\n";

    function folderApi(entries: object[], readFile: unknown) {
      const api = makeApi();
      routerMock.search = { path: "/nic/app" };
      api.resolvePath = vi.fn().mockResolvedValue({ entry: { kind: "ENTRY_KIND_DIRECTORY", path: "/nic/app" } });
      api.listDirectory = vi.fn().mockResolvedValue({ entries });
      api.readFile = readFile as typeof api.readFile;
      apiMock.current = api;
      return api;
    }
    const entries = [
      { kind: "ENTRY_KIND_DIRECTORY", name: "docs", path: "/nic/app/docs" },
      { kind: "ENTRY_KIND_FILE", name: "README.md", path: "/nic/app/README.md", size: "120" },
      { kind: "ENTRY_KIND_FILE", name: "main.go", path: "/nic/app/main.go", size: "40", contentHash: "sha256:abcdef0123456789" }
    ];

    it("gives the README the page: no list of the folder's own files, and the tree folded", async () => {
      const api = folderApi(entries, vi.fn(async (request: { path?: string }) => ({ data: request.path?.endsWith(".png") ? btoa("png") : btoa(readmeText) })));
      renderRoute(<SliceDetailPage />);
      // The first README render loads the Markdown renderer, which can take over
      // a second on a busy CI host.
      expect(await screen.findByRole("heading", { name: "Hello app" }, { timeout: 5000 })).toBeInTheDocument();
      expect(api.readFile).toHaveBeenCalledWith(expect.objectContaining({ path: "/nic/app/README.md" }));

      // No second list of what the file tree has: no Files column, no
      // kind, size or hash columns, no commit line.
      expect(screen.queryByRole("complementary", { name: "Files" })).toBeNull();
      expect(screen.queryByText("Content hash")).toBeNull();
      expect(screen.queryByText(/^Commit$/)).toBeNull();
      // The full tree is folded to the rail that opens it, and stays open once
      // someone opens it.
      const rail = screen.getByRole("button", { name: "Show files" });
      expect(rail.className).toMatch(/lg:flex/);
      expect(document.getElementById("slice-file-tree-panel")?.className).toMatch(/(^|\s)hidden(\s|$)/);
      fireEvent.click(rail);
      expect(document.getElementById("slice-file-tree-panel")?.className).toMatch(/(^|\s)block(\s|$)/);
      // A relative image is read from the repository, next to the README.
      await waitFor(() => expect(screen.getByRole("img", { name: "logo" })).toHaveAttribute("src", expect.stringMatching(/^data:image\/png;base64,/)));
      expect(api.readFile).toHaveBeenCalledWith(expect.objectContaining({ path: "/nic/app/logo.png", commitId: expect.any(String) }));
      expectHealthy();
    });

    it("shows the alt text for a relative image it cannot read, and loads a nested one from its folder", async () => {
      const text = "![gone](missing.png) ![shot](docs/shot.webp) ![web](https://example.com/x.png)\n";
      const readFile = vi.fn(async (request: { path?: string }) => {
        if (request.path === "/nic/app/README.md") {
          return { data: btoa(text) };
        }
        if (request.path === "/nic/app/docs/shot.webp") {
          return { data: btoa("webp-bytes") };
        }
        throw new Error("not found");
      });
      folderApi(entries, readFile);
      renderRoute(<SliceDetailPage />);

      expect(await screen.findByText("[gone]")).toBeInTheDocument();
      expect(screen.getByRole("img", { name: "shot" })).toHaveAttribute("src", `data:image/webp;base64,${btoa("webp-bytes")}`);
      // An image with a web address is the browser's to load.
      expect(screen.getByRole("img", { name: "web" })).toHaveAttribute("src", "https://example.com/x.png");
      expect(readFile).toHaveBeenCalledWith(expect.objectContaining({ path: "/nic/app/docs/shot.webp" }));
      expect(readFile).not.toHaveBeenCalledWith(expect.objectContaining({ path: expect.stringContaining("example.com") }));
    });

    it("loads a root-absolute image from the project root, like GitHub, and not from the folder", async () => {
      const readFile = vi.fn(async (request: { path?: string }) =>
        request.path === "/nic/app/README.md"
          ? { data: btoa("![logo](/Imagens/logo.png) ![out](/../secret.png)\n") }
          : { data: btoa("png-bytes") }
      );
      folderApi(entries, readFile);
      renderRoute(<SliceDetailPage />);

      const logo = await screen.findByRole("img", { name: "logo" });
      await waitFor(() => expect(logo).toHaveAttribute("src", `data:image/png;base64,${btoa("png-bytes")}`));
      // The slice includes /nic, so the project root is /nic.
      expect(readFile).toHaveBeenCalledWith(expect.objectContaining({ path: "/nic/Imagens/logo.png" }));
      // One that would leave the project is left alone, never requested.
      expect(readFile).not.toHaveBeenCalledWith(expect.objectContaining({ path: "/secret.png" }));
      expect(screen.getByRole("img", { name: "out" })).toHaveAttribute("src", "/../secret.png");
    });

    it("loads the images of a README opened as a file, and opens its links in the slice", async () => {
      routerMock.search = { path: "/nic/app/README.md" };
      const api = makeApi();
      api.resolvePath = vi.fn().mockResolvedValue({ entry: { kind: "ENTRY_KIND_FILE", path: "/nic/app/README.md", size: "60" } });
      api.readFile = vi.fn(async (request: { path?: string }) =>
        request.path === "/nic/app/README.md"
          ? { data: btoa("See the [guide](docs/guide.md).\n\n![logo](logo.png) ![root](/pics/r.png)\n") }
          : { data: btoa("png-bytes") }
      ) as typeof api.readFile;
      apiMock.current = api;
      renderRoute(<SliceDetailPage />);

      const logo = await screen.findByRole("img", { name: "logo" });
      await waitFor(() => expect(logo).toHaveAttribute("src", `data:image/png;base64,${btoa("png-bytes")}`));
      // A root-absolute image in the file view is read from the project root.
      await waitFor(() => expect(screen.getByRole("img", { name: "root" })).toHaveAttribute("src", expect.stringMatching(/^data:image\/png/)));
      expect(api.readFile).toHaveBeenCalledWith(expect.objectContaining({ path: "/nic/pics/r.png" }));
      fireEvent.click(screen.getByRole("link", { name: "guide" }));
      expect(routerMock.navigate).toHaveBeenCalledWith(
        expect.objectContaining({ search: { path: "/nic/app/docs/guide.md" } })
      );
    });

    it("opens relative links inside the slice and leaves other links to the browser", async () => {
      folderApi(entries, vi.fn().mockResolvedValue({ data: btoa(readmeText) }));
      renderRoute(<SliceDetailPage />);
      const guide = await screen.findByRole("link", { name: "guide" });
      fireEvent.click(guide);
      expect(routerMock.navigate).toHaveBeenCalledWith(
        expect.objectContaining({ search: { path: "/nic/app/docs/guide.md" } })
      );

      routerMock.navigate.mockClear();
      const external = screen.getByRole("link", { name: "the site" });
      const clicked = fireEvent.click(external);
      expect(clicked).toBe(true); // not prevented: the browser follows it
      expect(routerMock.navigate).not.toHaveBeenCalled();
    });

    it("offers Edit to writers and Open to readers", async () => {
      folderApi(entries, vi.fn().mockResolvedValue({ data: btoa(readmeText) }));
      renderRoute(<SliceDetailPage />);
      const open = await screen.findByRole("button", { name: "Open" });
      fireEvent.click(open);
      expect(routerMock.navigate).toHaveBeenCalledWith(
        expect.objectContaining({ search: { path: "/nic/app/README.md" } })
      );
    });

    it("links a README that is too large instead of loading it", async () => {
      const readFile = vi.fn();
      const big = entries.map((entry) => (entry.name === "README.md" ? { ...entry, size: String(300 * 1024) } : entry));
      folderApi(big, readFile);
      renderRoute(<SliceDetailPage />);
      expect(await screen.findByText(/too large to show here/)).toBeInTheDocument();
      expect(readFile).not.toHaveBeenCalledWith(expect.objectContaining({ path: "/nic/app/README.md" }));
    });

    it("says when the README cannot be loaded, and the file tree is still there", async () => {
      folderApi(entries, vi.fn().mockRejectedValue(new Error("boom")));
      renderRoute(<SliceDetailPage />);
      expect(await screen.findByRole("alert")).toHaveTextContent("Could not load README.md: boom");
      expect(screen.getByRole("button", { name: "Show files" })).toBeInTheDocument();
    });

    it("keeps the file table when the folder has no README", async () => {
      const api = folderApi(entries.filter((entry) => entry.name !== "README.md"), vi.fn());
      renderRoute(<SliceDetailPage />);
      expect(await screen.findByText("Content hash")).toBeInTheDocument();
      expect(screen.getByText(/^Commit$/)).toBeInTheDocument();
      expect(api.readFile).not.toHaveBeenCalledWith(expect.objectContaining({ path: "/nic/app/README.md" }));
    });

    it("shows the README of the one folder a folder holds, however deep", async () => {
      const api = folderApi([], vi.fn(async (request: { path?: string }) => ({ data: request.path?.endsWith(".png") ? btoa("png") : btoa(readmeText) })));
      routerMock.search = { path: "/sunny" };
      api.resolvePath = vi.fn().mockResolvedValue({ entry: { kind: "ENTRY_KIND_DIRECTORY", path: "/sunny" } });
      const tree: Record<string, object[]> = {
        "/sunny": [{ kind: "ENTRY_KIND_DIRECTORY", name: "macnativegames", path: "/sunny/macnativegames" }],
        "/sunny/macnativegames": [{ kind: "ENTRY_KIND_DIRECTORY", name: "app", path: "/sunny/macnativegames/app" }],
        "/sunny/macnativegames/app": [
          { kind: "ENTRY_KIND_FILE", name: "README.md", path: "/sunny/macnativegames/app/README.md", size: "120" },
          { kind: "ENTRY_KIND_FILE", name: "main.c", path: "/sunny/macnativegames/app/main.c", size: "40" }
        ]
      };
      api.listDirectory = vi.fn(async (request: { path?: string }) => ({ entries: tree[request.path ?? ""] ?? [] }));
      renderRoute(<SliceDetailPage />);
      expect(await screen.findByRole("heading", { name: "Hello app" }, { timeout: 5000 })).toBeInTheDocument();
      expect(api.readFile).toHaveBeenCalledWith(expect.objectContaining({ path: "/sunny/macnativegames/app/README.md" }));
      // It says where the README is.
      expect(screen.getByRole("heading", { name: "macnativegames/app/README.md" })).toBeInTheDocument();
      expect(screen.queryByRole("complementary", { name: "Files" })).toBeNull();
    });

    it("has the Files bar on a phone for a README folder at any path, not only the root", async () => {
      folderApi(entries, vi.fn().mockResolvedValue({ data: btoa(readmeText) }));
      renderRoute(<SliceDetailPage />);
      await screen.findByRole("heading", { name: "Hello app" });
      const bar = screen.getByRole("button", { name: /^Files/ });
      expect(bar).toHaveAttribute("aria-expanded", "false");
      expect(bar.className).toMatch(/lg:hidden/);
      expect(document.getElementById("slice-file-tree-panel")?.className).toMatch(/(^|\s)hidden(\s|$)/);
      fireEvent.click(bar);
      expect(bar).toHaveAttribute("aria-expanded", "true");
      expect(document.getElementById("slice-file-tree-panel")?.className).toMatch(/(^|\s)block(\s|$)/);
    });

    it("has no Files bar for a folder without a README: its listing is on the page", async () => {
      folderApi(entries.filter((entry) => entry.name !== "README.md"), vi.fn());
      renderRoute(<SliceDetailPage />);
      await screen.findByText("Content hash");
      expect(screen.queryByRole("button", { name: /^Files/ })).toBeNull();
    });

    it("keeps the file tree open where there is no README to give the page to", async () => {
      folderApi(entries.filter((entry) => entry.name !== "README.md"), vi.fn());
      renderRoute(<SliceDetailPage />);
      await screen.findByText("Content hash");
      expect(screen.getByRole("button", { name: "Show files" }).className).not.toMatch(/lg:flex/);
      expect(document.getElementById("slice-file-tree-panel")?.className).toMatch(/lg:block/);
    });
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
    api.inviteAccountMember = vi.fn().mockResolvedValue({});
    api.cancelAccountInvitation = vi.fn().mockResolvedValue({});
    api.listAccountInvitations = vi.fn().mockResolvedValue({
      invitations: [{ account: "gitslice", username: "pending-pat", role: "reader", invitedBy: "nic" }]
    });
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

    // New people are invited, not added.
    fireEvent.change(within(people).getByPlaceholderText("username"), { target: { value: "@alice" } });
    fireEvent.click(within(people).getByRole("button", { name: "Invite" }));
    await waitFor(() =>
      expect(api.inviteAccountMember).toHaveBeenLastCalledWith({ account: "gitslice", role: "writer", username: "alice" })
    );
    expect(await within(people).findByText("pending-pat")).toBeInTheDocument();
    fireEvent.click(within(people).getByRole("button", { name: "Cancel invitation" }));
    await waitFor(() =>
      expect(api.cancelAccountInvitation).toHaveBeenCalledWith({ account: "gitslice", username: "pending-pat" })
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

  it("names the slice's CI daemon, even for someone who cannot list it", async () => {
    const api = makeApi();
    api.resolveSlice = vi.fn().mockResolvedValue({
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
      ciDaemonId: "agent_f83ad2fa",
      ciDaemonName: "Main CI"
    });
    // The viewer lists no daemons: the agent that runs CI is not theirs.
    api.listDaemons = vi.fn().mockResolvedValue({ daemons: [] });
    apiMock.current = api;

    renderRoute(<SliceSettingsPage />);

    expect(await screen.findByText("Main CI")).toBeInTheDocument();
    expect(screen.getByText("agent_f83ad2fa")).toBeInTheDocument();
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
}
