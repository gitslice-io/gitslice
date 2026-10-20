import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { CopyMoveProvider } from "./CopyMove";
import { DirectoryHeader } from "./DirectoryHeader";
import { EditableFileView } from "./EditableFileView";
import { SliceDirectoryTable } from "./SliceDirectoryTable";

const api = vi.hoisted(() => ({
  listDirectory: vi.fn(),
  resolvePath: vi.fn()
}));

vi.mock("../../api/useApi", () => ({ useApi: () => api }));

const included = ["/src"];
const canModify = (path: string) => path.startsWith("/src/");

function notFound() {
  return Object.assign(new Error("path not found"), {});
}

function renderFile(stage = vi.fn()) {
  render(
    <CopyMoveProvider canModify={canModify} commitId="c1" stage={stage}>
      <EditableFileView
        commitId="commit_12345678"
        fileContent="package a"
        fileData=""
        includedPaths={included}
        onOpenHistory={vi.fn()}
        onStageEdit={vi.fn()}
        pendingEdits={[]}
        selectedPath="/src/a.go"
      />
    </CopyMoveProvider>
  );
  return stage;
}

function openDialog(item: string) {
  fireEvent.click(screen.getByRole("button", { name: "File actions" }));
  fireEvent.click(screen.getByRole("menuitem", { name: item }));
}

beforeEach(() => {
  api.listDirectory.mockReset();
  api.resolvePath.mockReset();
});
afterEach(cleanup);

describe("copy and move menus", () => {
  it("are not offered without the provider", () => {
    render(
      <EditableFileView
        commitId="commit_12345678"
        fileContent="package a"
        fileData=""
        includedPaths={included}
        onOpenHistory={vi.fn()}
        onStageEdit={vi.fn()}
        pendingEdits={[]}
        selectedPath="/src/a.go"
      />
    );
    fireEvent.click(screen.getByRole("button", { name: "File actions" }));
    expect(screen.queryByRole("menuitem", { name: "Copy to…" })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Move to…" })).not.toBeInTheDocument();
  });

  it("disable moving a top-level slice folder but still allow copying it", () => {
    render(
      <CopyMoveProvider canModify={canModify} commitId="c1" stage={vi.fn()}>
        <DirectoryHeader
          commitId="c1"
          includedPaths={included}
          onStageEdit={vi.fn()}
          selectedPath="/src"
        />
      </CopyMoveProvider>
    );
    fireEvent.click(screen.getByRole("button", { name: "Directory actions" }));
    expect(screen.getByRole("menuitem", { name: "Copy to…" })).toBeEnabled();
    expect(screen.getByRole("menuitem", { name: "Move to…" })).toBeDisabled();
  });

  it("appear on directory rows", () => {
    render(
      <CopyMoveProvider canModify={canModify} commitId="c1" stage={vi.fn()}>
        <SliceDirectoryTable
          entries={[{ path: "/src/pkg", name: "pkg", kind: "ENTRY_KIND_DIRECTORY" }]}
          includedPaths={included}
          onSelectPath={vi.fn()}
          onStageEdit={vi.fn()}
        />
      </CopyMoveProvider>
    );
    fireEvent.click(screen.getByRole("button", { name: "Actions for pkg" }));
    expect(screen.getByRole("menuitem", { name: "Copy to…" })).toBeEnabled();
    expect(screen.getByRole("menuitem", { name: "Move to…" })).toBeEnabled();
  });
});

describe("copy and move dialog", () => {
  it("moves to a new path with one rename", async () => {
    api.resolvePath.mockRejectedValue(notFound());
    const stage = renderFile();
    openDialog("Move to…");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "/lib/b.go" } });
    fireEvent.click(screen.getByRole("button", { name: "Move" }));
    // lib is outside the included /src, so it is refused before any request.
    expect(await screen.findByRole("alert")).toHaveTextContent(/outside/);
    expect(stage).not.toHaveBeenCalled();

    fireEvent.change(screen.getByRole("textbox"), { target: { value: "b.go" } });
    fireEvent.click(screen.getByRole("button", { name: "Move" }));
    await waitFor(() =>
      expect(stage).toHaveBeenCalledWith([
        { kind: "rename", oldPath: "/src/a.go", path: "/src/b.go" }
      ])
    );
  });

  it("puts the item inside an existing folder", async () => {
    api.resolvePath.mockImplementation(async ({ path }: { path: string }) => {
      if (path === "/src/pkg") {
        return { entry: { path, kind: "ENTRY_KIND_DIRECTORY" } };
      }
      throw notFound();
    });
    const stage = renderFile();
    openDialog("Move to…");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "pkg" } });
    fireEvent.click(screen.getByRole("button", { name: "Move" }));
    await waitFor(() =>
      expect(stage).toHaveBeenCalledWith([
        { kind: "rename", oldPath: "/src/a.go", path: "/src/pkg/a.go" }
      ])
    );
  });

  it("refuses to overwrite an existing file", async () => {
    api.resolvePath.mockImplementation(async ({ path }: { path: string }) => ({
      entry: { path, kind: "ENTRY_KIND_FILE" }
    }));
    const stage = renderFile();
    openDialog("Copy to…");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "b.go" } });
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/already exists/);
    expect(stage).not.toHaveBeenCalled();
  });

  it("copies a file by reference to its blob", async () => {
    api.resolvePath.mockImplementation(async ({ path }: { path: string }) => {
      if (path === "/src/a.go") {
        return {
          entry: {
            path,
            kind: "ENTRY_KIND_FILE",
            blobId: "blob1",
            contentHash: "hash1"
          }
        };
      }
      throw notFound();
    });
    const stage = renderFile();
    openDialog("Copy to…");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "copy.go" } });
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    await waitFor(() =>
      expect(stage).toHaveBeenCalledWith([
        {
          kind: "write",
          path: "/src/copy.go",
          isNew: true,
          blobId: "blob1",
          contentHash: "hash1"
        }
      ])
    );
  });

  it("copies a folder with its contents in one batch", async () => {
    api.resolvePath.mockRejectedValue(notFound());
    api.listDirectory.mockResolvedValue({
      recursive: true,
      entries: [
        { path: "/src/pkg/x.go", kind: "ENTRY_KIND_FILE", blobId: "b", contentHash: "h" }
      ]
    });
    const stage = vi.fn();
    render(
      <CopyMoveProvider canModify={canModify} commitId="c1" stage={stage}>
        <DirectoryHeader
          commitId="c1"
          includedPaths={["/src", "/lib"]}
          onStageEdit={vi.fn()}
          selectedPath="/src/pkg"
        />
      </CopyMoveProvider>
    );
    fireEvent.click(screen.getByRole("button", { name: "Directory actions" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Copy to…" }));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "pkg2" } });
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    await waitFor(() =>
      expect(stage).toHaveBeenCalledWith([
        { kind: "mkdir", path: "/src/pkg2" },
        { kind: "write", path: "/src/pkg2/x.go", isNew: true, blobId: "b", contentHash: "h" }
      ])
    );
  });
});
