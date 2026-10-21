import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
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
    expect(within(rows[0]).getByText("alice")).toBeInTheDocument();
    expect(within(rows[2]).getByText("bob")).toBeInTheDocument();

    // The commit links to the changeset that landed it, on this file's diff;
    // one no changeset landed is plain text.
    expect(within(rows[0]).getByRole("link")).toHaveAttribute("href", "/cs/0123456789?file=acme/app/notes.txt");
    expect(within(rows[0]).getByRole("link")).toHaveAttribute("title", expect.stringContaining("Add the notes"));
    expect(within(rows[2]).queryByRole("link")).not.toBeInTheDocument();
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
