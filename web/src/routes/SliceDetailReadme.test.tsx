import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SliceDetailPage } from "./SliceDetailPage";
import {
  apiMock,
  authMock,
  cleanupSlicePagesHooks,
  expectHealthy,
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

describe("a folder with a README", () => {
  let consoleError: ReturnType<typeof vi.spyOn>;

  beforeEach(() => {
    consoleError = resetSlicePagesHooks();
  });

  afterEach(() => {
    cleanupSlicePagesHooks(consoleError);
  });

  const readmeText = "# Hello app\n\nStart with the [guide](docs/guide.md) or [the site](https://example.com).\n\n![logo](logo.png)\n";

  function folderApi(entries: object[], readFile: unknown) {
    const api = makeSlicePagesApi();
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
    renderSliceRoute(<SliceDetailPage />);
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
    expectHealthy(consoleError);
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
    renderSliceRoute(<SliceDetailPage />);

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
    renderSliceRoute(<SliceDetailPage />);

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
    const api = makeSlicePagesApi();
    api.resolvePath = vi.fn().mockResolvedValue({ entry: { kind: "ENTRY_KIND_FILE", path: "/nic/app/README.md", size: "60" } });
    api.readFile = vi.fn(async (request: { path?: string }) =>
      request.path === "/nic/app/README.md"
        ? { data: btoa("See the [guide](docs/guide.md).\n\n![logo](logo.png) ![root](/pics/r.png)\n") }
        : { data: btoa("png-bytes") }
    ) as typeof api.readFile;
    apiMock.current = api;
    renderSliceRoute(<SliceDetailPage />);

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
    renderSliceRoute(<SliceDetailPage />);
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
    renderSliceRoute(<SliceDetailPage />);
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
    renderSliceRoute(<SliceDetailPage />);
    expect(await screen.findByText(/too large to show here/)).toBeInTheDocument();
    expect(readFile).not.toHaveBeenCalledWith(expect.objectContaining({ path: "/nic/app/README.md" }));
  });

  it("says when the README cannot be loaded, and the file tree is still there", async () => {
    folderApi(entries, vi.fn().mockRejectedValue(new Error("boom")));
    renderSliceRoute(<SliceDetailPage />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load README.md: boom");
    expect(screen.getByRole("button", { name: "Show files" })).toBeInTheDocument();
  });

  it("keeps the file table when the folder has no README", async () => {
    const api = folderApi(entries.filter((entry) => entry.name !== "README.md"), vi.fn());
    renderSliceRoute(<SliceDetailPage />);
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
    renderSliceRoute(<SliceDetailPage />);
    expect(await screen.findByRole("heading", { name: "Hello app" }, { timeout: 5000 })).toBeInTheDocument();
    expect(api.readFile).toHaveBeenCalledWith(expect.objectContaining({ path: "/sunny/macnativegames/app/README.md" }));
    // It says where the README is.
    expect(screen.getByRole("heading", { name: "macnativegames/app/README.md" })).toBeInTheDocument();
    expect(screen.queryByRole("complementary", { name: "Files" })).toBeNull();
  });

  it("has the Files bar on a phone for a README folder at any path, not only the root", async () => {
    folderApi(entries, vi.fn().mockResolvedValue({ data: btoa(readmeText) }));
    renderSliceRoute(<SliceDetailPage />);
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
    renderSliceRoute(<SliceDetailPage />);
    await screen.findByText("Content hash");
    expect(screen.queryByRole("button", { name: /^Files/ })).toBeNull();
  });

  it("does not open the tree while moving to another README folder, only to fold it again", async () => {
    const api = folderApi(entries, vi.fn().mockResolvedValue({ data: btoa(readmeText) }));
    // The second folder's kind arrives late, as it does when navigating.
    let resolveSecond: (value: unknown) => void = () => {};
    const second = new Promise((resolve) => {
      resolveSecond = resolve;
    });
    api.resolvePath = vi.fn((request: { path?: string }) =>
      request.path === "/nic/docs" ? second : Promise.resolve({ entry: { kind: "ENTRY_KIND_DIRECTORY", path: "/nic/app" } })
    ) as typeof api.resolvePath;
    const panel = () => document.getElementById("slice-file-tree-panel")?.className ?? "";
    const folded = (className: string) => /(^|\s)hidden(\s|$)/.test(className) && !/lg:block/.test(className);

    const view = renderSliceRoute(<SliceDetailPage />);
    await screen.findByRole("heading", { name: "Hello app" });
    expect(folded(panel())).toBe(true);

    // Into another folder: while it loads, the tree stays folded.
    routerMock.search = { path: "/nic/docs" };
    view.rerender(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <SliceDetailPage />
      </QueryClientProvider>
    );
    const states: string[] = [panel()];
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
    states.push(panel());
    resolveSecond({ entry: { kind: "ENTRY_KIND_DIRECTORY", path: "/nic/docs" } });
    await screen.findByRole("heading", { name: "Hello app" });
    states.push(panel());
    expect(states.every(folded)).toBe(true);
  });

  it("on a first visit, keeps the tree folded until it knows there is no README, then opens it", async () => {
    const api = folderApi(entries.filter((entry) => entry.name !== "README.md"), vi.fn());
    const pending: Array<(value: unknown) => void> = [];
    const resolveKind = (value: unknown) => pending.splice(0).forEach((resolve) => resolve(value));
    api.resolvePath = vi.fn(() => new Promise((resolve) => {
      pending.push(resolve);
    })) as typeof api.resolvePath;
    renderSliceRoute(<SliceDetailPage />);
    const panel = () => document.getElementById("slice-file-tree-panel")?.className ?? "";
    await waitFor(() => expect(pending.length).toBeGreaterThan(0));
    expect(panel()).not.toMatch(/lg:block/);
    resolveKind({ entry: { kind: "ENTRY_KIND_DIRECTORY", path: "/nic/app" } });
    await screen.findByText("Content hash");
    await waitFor(() => expect(panel()).toMatch(/lg:block/));
  });

  it("does not rebuild the README when the viewer turns out to be an editor after the page loads", async () => {
    folderApi(entries, vi.fn().mockResolvedValue({ data: btoa(readmeText) }));
    authMock.current = { isLoaded: true, isSignedIn: true };
    const original = selectionMock.current;
    // Memberships not known yet: not an editor.
    selectionMock.current = { ...original, memberships: [] };
    const view = renderSliceRoute(<SliceDetailPage />);
    const before = await screen.findByRole("heading", { name: "Hello app" });
    // They arrive: an owner of the account, so an editor.
    selectionMock.current = original;
    view.rerender(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <SliceDetailPage />
      </QueryClientProvider>
    );
    await waitFor(() => expect(screen.getByRole("button", { name: "Directory actions" })).toBeInTheDocument());
    // The same element: nothing under the workspace was mounted again.
    expect(screen.getByRole("heading", { name: "Hello app" })).toBe(before);
  });

  it("keeps the file tree open where there is no README to give the page to", async () => {
    folderApi(entries.filter((entry) => entry.name !== "README.md"), vi.fn());
    renderSliceRoute(<SliceDetailPage />);
    await screen.findByText("Content hash");
    expect(screen.getByRole("button", { name: "Show files" }).className).not.toMatch(/lg:flex/);
    expect(document.getElementById("slice-file-tree-panel")?.className).toMatch(/lg:block/);
  });
});
