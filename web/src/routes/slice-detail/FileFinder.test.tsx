import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { FileFinder } from "./FileFinder";

const file = (path: string) => ({ kind: "ENTRY_KIND_FILE", path });
const files = [
  "/gitslice/gitslice/README.md",
  "/gitslice/gitslice/go.mod",
  "/gitslice/gitslice/web/src/routes/ChangesetsPage.tsx",
  "/gitslice/gitslice/web/src/routes/ChangesetDetailPage.tsx",
  "/gitslice/gitslice/web/src/components/diff/DiffViewer.tsx",
  "/gitslice/gitslice/service/changeset.go"
];

function setup(listDirectory = vi.fn().mockResolvedValue({ recursive: true, entries: [{ kind: "ENTRY_KIND_DIRECTORY", path: "/gitslice/gitslice/web" }, ...files.map(file)] })) {
  const onClose = vi.fn();
  const onSelectPath = vi.fn();
  const view = render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <FileFinder
        api={{ listDirectory } as never}
        commitId="commit1"
        includedPaths={["/gitslice/gitslice"]}
        onClose={onClose}
        onSelectPath={onSelectPath}
        slice={{ account: "gitslice", slice: "gitslice" }}
      />
    </QueryClientProvider>
  );
  return { listDirectory, onClose, onSelectPath, view };
}

const box = () => screen.getByRole("combobox", { name: "Find a file by name" });
const options = () => screen.getAllByRole("option");
// An option's path as shown (its text also holds the file-type badge).
const names = () => options().map((option) => option.querySelector("[title]")?.getAttribute("title"));

beforeEach(() => {
  Element.prototype.scrollIntoView = vi.fn();
});
afterEach(cleanup);

describe("FileFinder", () => {
  it("lists the slice's files once and shows the shallowest first, without the folder they share", async () => {
    const { listDirectory } = setup();
    expect(screen.getByText("Listing the slice's files…")).toBeInTheDocument();
    await screen.findAllByRole("option");

    expect(listDirectory).toHaveBeenCalledTimes(1);
    expect(listDirectory).toHaveBeenCalledWith(expect.objectContaining({ commitId: "commit1", path: "/gitslice/gitslice", recursive: true }));
    expect(names()).toEqual([
      "go.mod",
      "README.md",
      "service/changeset.go",
      "web/src/routes/ChangesetDetailPage.tsx",
      "web/src/routes/ChangesetsPage.tsx",
      "web/src/components/diff/DiffViewer.tsx"
    ]);
    // A folder is not a file.
    expect(screen.getByText("6 files")).toBeInTheDocument();
    expect(box()).toHaveFocus();
  });

  it("ranks as you type, picks out the letters, and opens the best match on Enter", async () => {
    const { onClose, onSelectPath } = setup();
    await screen.findAllByRole("option");

    fireEvent.change(box(), { target: { value: "csdp" } });
    expect(names()[0]).toBe("web/src/routes/ChangesetDetailPage.tsx");
    expect(screen.getByText(/of 6 files/)).toBeInTheDocument();
    // The matched letters are marked, in the name.
    const first = options()[0];
    expect([...first.querySelectorAll("mark")].map((mark) => mark.textContent).join("")).toBe("CsDP");
    expect(first).toHaveAttribute("aria-selected", "true");

    fireEvent.keyDown(box(), { key: "Enter" });
    // The canonical path, not the shortened one.
    expect(onSelectPath).toHaveBeenCalledWith("/gitslice/gitslice/web/src/routes/ChangesetDetailPage.tsx");
    expect(onClose).toHaveBeenCalled();
  });

  it("moves through the results with the arrows, wrapping at the ends", async () => {
    const { onSelectPath } = setup();
    await screen.findAllByRole("option");
    fireEvent.change(box(), { target: { value: "changeset" } });
    const total = options().length;
    expect(total).toBeGreaterThan(2);
    const selected = () => options().findIndex((option) => option.getAttribute("aria-selected") === "true");

    expect(selected()).toBe(0);
    fireEvent.keyDown(box(), { key: "ArrowDown" });
    expect(selected()).toBe(1);
    expect(box()).toHaveAttribute("aria-activedescendant", options()[1].id);
    fireEvent.keyDown(box(), { key: "ArrowUp" });
    fireEvent.keyDown(box(), { key: "ArrowUp" });
    expect(selected()).toBe(total - 1);
    fireEvent.keyDown(box(), { key: "ArrowDown" });
    expect(selected()).toBe(0);
    fireEvent.keyDown(box(), { key: "n", ctrlKey: true });
    fireEvent.keyDown(box(), { key: "p", ctrlKey: true });
    fireEvent.keyDown(box(), { key: "ArrowDown" });
    fireEvent.keyDown(box(), { key: "Enter" });
    expect(onSelectPath).toHaveBeenCalledTimes(1);
    expect(onSelectPath.mock.calls[0][0]).toBe(`/gitslice/gitslice/${names()[1]}`);
    // A new query starts from the best match again.
    fireEvent.change(box(), { target: { value: "diff" } });
    expect(selected()).toBe(0);
  });

  it("opens a file by clicking it", async () => {
    const { onClose, onSelectPath } = setup();
    fireEvent.click((await screen.findByText("go.mod")).closest("li") as HTMLElement);
    expect(onSelectPath).toHaveBeenCalledWith("/gitslice/gitslice/go.mod");
    expect(onClose).toHaveBeenCalled();
  });

  it("closes on Escape and on a click outside, but not on a click inside", async () => {
    const { onClose, view } = setup();
    await screen.findAllByRole("option");
    fireEvent.mouseDown(screen.getByRole("dialog", { name: "Find file" }));
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.keyDown(box(), { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
    fireEvent.mouseDown(view.container.firstElementChild as HTMLElement);
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("says when nothing matches, and when the slice has no files", async () => {
    setup();
    await screen.findAllByRole("option");
    fireEvent.change(box(), { target: { value: "zzzq" } });
    expect(screen.getByText("No file matches “zzzq”.")).toBeInTheDocument();
    fireEvent.keyDown(box(), { key: "Enter" });
    cleanup();

    setup(vi.fn().mockResolvedValue({ recursive: true, entries: [] }));
    expect(await screen.findByText("This slice has no files.")).toBeInTheDocument();
  });

  it("owns up to a partial listing instead of saying the slice is empty", async () => {
    // A server too old to list recursively returns one level.
    setup(vi.fn().mockResolvedValue({ entries: [file("/gitslice/gitslice/top.md")] }));
    expect(await screen.findByText("Could not list this slice's files.")).toBeInTheDocument();
    expect(screen.queryByText("This slice has no files.")).toBeNull();
    expect(screen.getByText("0 files (not all listed)")).toBeInTheDocument();
  });

  it("shows a failed listing with a way to try again", async () => {
    const listDirectory = vi
      .fn()
      .mockRejectedValueOnce(new Error("gateway timeout"))
      .mockRejectedValueOnce(new Error("gateway timeout"))
      .mockResolvedValue({ recursive: true, entries: files.map(file) });
    setup(listDirectory);
    expect(await screen.findByText(/Could not list the files: gateway timeout/, undefined, { timeout: 3000 })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(options()).toHaveLength(6));
    expect(within(screen.getByRole("listbox")).getByText("go.mod")).toBeInTheDocument();
  });
});
