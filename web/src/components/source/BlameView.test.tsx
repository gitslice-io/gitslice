import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SourceCodeViewer } from "./SourceCodeViewer";

const api = vi.hoisted(() => ({ blameFile: vi.fn() }));

vi.mock("../../api/useApi", () => ({ useApi: () => api }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    params,
    search,
    to,
    ...rest
  }: {
    children: ReactNode;
    params?: { id?: string };
    search?: { file?: string };
    to: string;
  }) => (
    <a {...(rest as Record<string, unknown>)} href={`${to.replace("$id", params?.id ?? "")}${search?.file ? `?file=${search.file}` : ""}`}>
      {children}
    </a>
  )
}));

const slice = { account: "acme", slice: "app" };
const code = "alpha\nbeta\ngamma\ndelta\n";
const blame = {
  commitId: "sha256:head",
  path: "/acme/app/notes.txt",
  lineCount: 4,
  ranges: [
    { startLine: 1, lineCount: 2, commitId: "sha256:aaaaaaaaaaaa" },
    { startLine: 3, lineCount: 1, commitId: "sha256:bbbbbbbbbbbb" },
    { startLine: 4, lineCount: 1, commitId: "sha256:aaaaaaaaaaaa" }
  ],
  commits: [
    {
      id: "sha256:aaaaaaaaaaaa",
      author: "alice",
      message: "Add the notes\n\nlonger body",
      createdAt: "2026-10-01T00:00:00Z",
      changesetId: "cs_0123456789abcdef0123"
    },
    { id: "sha256:bbbbbbbbbbbb", author: "bob", message: "Fix a line", createdAt: "2026-10-05T00:00:00Z" }
  ]
};

function renderViewer(options: { slice?: typeof slice; path?: string; code?: string } = {}) {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <SourceCodeViewer
        code={options.code ?? code}
        fill
        path={options.path ?? "/acme/app/notes.txt"}
        repo={{ commitId: "sha256:head", onOpenPath: vi.fn(), slice: "slice" in options ? options.slice : slice }}
      />
    </QueryClientProvider>
  );
}

beforeEach(() => {
  api.blameFile.mockReset();
});
afterEach(cleanup);

describe("blame on a file", () => {
  it("is offered only for a file read through a slice", () => {
    renderViewer({ slice: undefined });
    expect(screen.queryByRole("button", { name: "Blame" })).not.toBeInTheDocument();
  });

  it("does nothing until it is turned on, then shows who last changed each run of lines", async () => {
    api.blameFile.mockResolvedValue(blame);
    renderViewer();
    expect(api.blameFile).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Blame" }));
    const gutter = await screen.findByLabelText("Blame");
    expect(api.blameFile).toHaveBeenCalledWith({ path: "/acme/app/notes.txt", slice });
    expect(screen.getByRole("button", { name: "Blame" })).toHaveAttribute("aria-pressed", "true");

    const rows = within(gutter).getAllByRole("row");
    expect(rows).toHaveLength(4);
    // Details appear once per run: lines 1 and 2 are one commit.
    expect(within(rows[0]).getByText("aaaaaaaa")).toBeInTheDocument();
    expect(within(rows[1]).queryByText("aaaaaaaa")).not.toBeInTheDocument();
    expect(within(rows[2]).getByText("bbbbbbbb")).toBeInTheDocument();
    expect(within(rows[3]).getByText("aaaaaaaa")).toBeInTheDocument();
    // Each block shows its commit's subject on its first line, inline, and on
    // its second the author and the change's order (oldest first).
    expect(within(rows[0]).getByText("Add the notes")).toBeInTheDocument();
    expect(within(rows[1]).getByText("alice")).toBeInTheDocument();
    expect(within(rows[1]).getByText("1/2")).toHaveAttribute("title", "the oldest of 2 changes still in this file");
    expect(within(rows[2]).getByText("Fix a line")).toBeInTheDocument();
    // A one-line block has no second line: its author is in the title.
    expect(within(rows[2]).queryByText("bob")).not.toBeInTheDocument();
    expect(within(rows[2]).getByText("Fix a line")).toHaveAttribute("title", expect.stringContaining("bob"));
    expect(within(rows[2]).getByText("Fix a line")).toHaveAttribute("title", expect.stringContaining("the newest of 2"));
    // The change date shows on the block's first line.
    expect(within(rows[0]).getByText(/Oct 1/)).toBeInTheDocument();
    expect(within(rows[2]).getByText(/Oct 5/)).toBeInTheDocument();
    // Every line has an age bar: the newer change is darker.
    const age = (row: HTMLElement) => Number((row.querySelector("[data-age]") as HTMLElement).style.opacity);
    expect(age(rows[2])).toBeGreaterThan(age(rows[0]));
    expect(age(rows[1])).toBe(age(rows[0]));

    // The commit links to the changeset that landed it, on this file's diff;
    // one no changeset landed is plain text.
    expect(within(rows[0]).getByRole("link")).toHaveAttribute("href", "/cs/0123456789?file=acme/app/notes.txt");
    expect(within(rows[0]).getByRole("link")).toHaveAttribute("title", expect.stringContaining("Add the notes"));
    expect(within(rows[2]).queryByRole("link")).not.toBeInTheDocument();
  });

  it("shows a tapped block's change in full, for phones, and closes it", async () => {
    api.blameFile.mockResolvedValue(blame);
    renderViewer();
    fireEvent.click(screen.getByRole("button", { name: "Blame" }));
    const rows = within(await screen.findByLabelText("Blame")).getAllByRole("row");
    expect(screen.queryByRole("region", { name: "Change" })).not.toBeInTheDocument();

    // Any line of a block selects it.
    fireEvent.click(rows[1]);
    const details = screen.getByRole("region", { name: "Change" });
    expect(within(details).getByText("Add the notes")).toBeInTheDocument();
    expect(within(details).getByText("alice")).toBeInTheDocument();
    expect(within(details).getByText("the oldest of 2 changes still in this file")).toBeInTheDocument();
    expect(within(details).getByRole("link")).toHaveAttribute("href", "/cs/0123456789?file=acme/app/notes.txt");

    // Another block replaces it; the same block again, or Close, clears it.
    fireEvent.click(rows[2]);
    expect(within(screen.getByRole("region", { name: "Change" })).getByText("Fix a line")).toBeInTheDocument();
    fireEvent.click(rows[2]);
    expect(screen.queryByRole("region", { name: "Change" })).not.toBeInTheDocument();
    fireEvent.click(rows[0]);
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("region", { name: "Change" })).not.toBeInTheDocument();
  });

  it("marks the selected change on the code itself, over each place it changed", async () => {
    api.blameFile.mockResolvedValue(blame);
    renderViewer();
    fireEvent.click(screen.getByRole("button", { name: "Blame" }));
    const rows = within(await screen.findByLabelText("Blame")).getAllByRole("row");
    const bands = () => Array.from(document.querySelectorAll<HTMLElement>("[data-blame-highlight] [data-lines]"));
    expect(bands()).toHaveLength(0);

    // The first commit changed lines 1-2 and line 4; a tap anywhere in its
    // first block marks both places, at the code's line height and padding.
    fireEvent.click(rows[1]);
    expect(bands().map((band) => band.dataset.lines)).toEqual(["1-2", "4-4"]);
    expect(bands().map((band) => [band.style.top, band.style.height])).toEqual([
      ["16px", "48px"],
      ["88px", "24px"]
    ]);

    // Another change moves the mark; the same one again clears it.
    fireEvent.click(rows[2]);
    expect(bands().map((band) => band.dataset.lines)).toEqual(["3-3"]);
    fireEvent.click(rows[2]);
    expect(bands()).toHaveLength(0);
  });

  describe("when the page owns the blame state (the address)", () => {
    function renderControlled(options: { on?: boolean; at?: string; onChange?: (on: boolean, at: string) => void } = {}) {
      const onChange = options.onChange ?? vi.fn();
      render(
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          <SourceCodeViewer
            blame={{ at: options.at ?? "", on: options.on ?? true, onChange }}
            code={code}
            fill
            path="/acme/app/notes.txt"
            repo={{ commitId: "sha256:head", onOpenPath: vi.fn(), slice }}
          />
        </QueryClientProvider>
      );
      return onChange;
    }

    it("has no Blame button of its own: the page's menu has it", () => {
      api.blameFile.mockResolvedValue(blame);
      renderControlled({ on: false });
      expect(screen.queryByRole("button", { name: "Blame" })).not.toBeInTheDocument();
      expect(api.blameFile).not.toHaveBeenCalled();
    });

    it("shows the blame the address says is on, with the change it names marked", async () => {
      api.blameFile.mockResolvedValue(blame);
      renderControlled({ at: "bbbbbbbbbbbb" });
      expect(await screen.findByLabelText("Blame")).toBeInTheDocument();
      // Selected from the address alone, nothing clicked.
      await waitFor(() => expect(document.querySelectorAll("[data-blame-highlight] [data-lines]")).toHaveLength(1));
      expect(document.querySelector("[data-blame-highlight] [data-lines]")).toHaveAttribute("data-lines", "3-3");
    });

    it("reports a tapped block's change to the page, as the short id the address carries, and clears it on a second tap", async () => {
      api.blameFile.mockResolvedValue(blame);
      const onChange = renderControlled();
      const rows = within(await screen.findByLabelText("Blame")).getAllByRole("row");
      fireEvent.click(rows[0]);
      expect(onChange).toHaveBeenLastCalledWith(true, "aaaaaaaaaaaa");
    });

    it("keeps the scroll position for the address, and restores it when the page comes back to it", async () => {
      api.blameFile.mockResolvedValue(blame);
      const scrollTo = vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
      Object.defineProperty(window, "scrollY", { configurable: true, value: 640 });
      try {
        renderControlled();
        const rows = within(await screen.findByLabelText("Blame")).getAllByRole("row");
        // Following the commit's link out of the blame leaves the page.
        fireEvent.click(within(rows[0]).getByRole("link"));
        const saved = JSON.parse(sessionStorage.getItem("gitslice.blame.scroll") ?? "null");
        expect(saved).toEqual({ href: window.location.href, y: 640 });

        // Back at the same address, once the blame is drawn: restored, once.
        cleanup();
        scrollTo.mockClear();
        renderControlled();
        await screen.findByLabelText("Blame");
        await waitFor(() => expect(scrollTo).toHaveBeenCalledWith(0, 640));
        expect(sessionStorage.getItem("gitslice.blame.scroll")).toBeNull();
      } finally {
        scrollTo.mockRestore();
        Reflect.deleteProperty(window, "scrollY");
        sessionStorage.clear();
      }
    });

    it("does not restore a position left at another address", async () => {
      api.blameFile.mockResolvedValue(blame);
      const scrollTo = vi.spyOn(window, "scrollTo").mockImplementation(() => undefined);
      sessionStorage.setItem("gitslice.blame.scroll", JSON.stringify({ href: "http://other.example/x", y: 900 }));
      try {
        renderControlled();
        await screen.findByLabelText("Blame");
        expect(scrollTo).not.toHaveBeenCalled();
      } finally {
        scrollTo.mockRestore();
        sessionStorage.clear();
      }
    });
  });

  it("turns off again, back to the plain file", async () => {
    api.blameFile.mockResolvedValue(blame);
    renderViewer();
    fireEvent.click(screen.getByRole("button", { name: "Blame" }));
    await screen.findByLabelText("Blame");
    fireEvent.click(screen.getByRole("button", { name: "Blame" }));
    expect(screen.queryByLabelText("Blame")).not.toBeInTheDocument();
    expect(screen.getByText(/alpha/)).toBeInTheDocument();
  });

  it("says so when the file has moved on since the page loaded, and shows no gutter", async () => {
    api.blameFile.mockResolvedValue({ ...blame, lineCount: 9 });
    renderViewer();
    fireEvent.click(screen.getByRole("button", { name: "Blame" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/changed since the page loaded/);
    expect(screen.queryByLabelText("Blame")).not.toBeInTheDocument();
    expect(screen.getByText(/alpha/)).toBeInTheDocument();
  });

  it("shows the server's reason when blame is unavailable, and keeps the file", async () => {
    api.blameFile.mockRejectedValue(new Error("file is too large to blame"));
    renderViewer();
    fireEvent.click(screen.getByRole("button", { name: "Blame" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("file is too large to blame");
    expect(screen.getByText(/alpha/)).toBeInTheDocument();
  });

  it("shows a markdown file's lines, not its preview, while blaming", async () => {
    api.blameFile.mockResolvedValue({ ...blame, lineCount: 2, ranges: [{ startLine: 1, lineCount: 2, commitId: "sha256:aaaaaaaaaaaa" }] });
    renderViewer({ code: "# Title\ntext\n", path: "/acme/app/README.md" });
    fireEvent.click(screen.getByRole("button", { name: "Blame" }));
    const gutter = await screen.findByLabelText("Blame");
    expect(within(gutter).getAllByRole("row")).toHaveLength(2);
    expect(screen.queryByRole("heading", { name: "Title" })).not.toBeInTheDocument();
  });

  it("notes a truncated history", async () => {
    api.blameFile.mockResolvedValue({ ...blame, truncated: true });
    renderViewer();
    fireEvent.click(screen.getByRole("button", { name: "Blame" }));
    expect(await screen.findByText(/long history/)).toBeInTheDocument();
  });
});
