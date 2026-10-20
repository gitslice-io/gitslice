import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { CopyMoveProvider } from "./CopyMove";
import { DirectoryHeader } from "./DirectoryHeader";
import { EditableFileView } from "./EditableFileView";
import { SliceDirectoryTable } from "./SliceDirectoryTable";

const api = vi.hoisted(() => ({
  createChangeset: vi.fn(),
  listDirectory: vi.fn(),
  listSlices: vi.fn(),
  resolvePath: vi.fn(),
  updateChangeset: vi.fn(),
  uploadBlob: vi.fn()
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
  for (const fn of Object.values(api)) {
    fn.mockReset();
  }
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
    api.listSlices.mockResolvedValue({ slices: [] });
    const stage = renderFile();
    openDialog("Move to…");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "/lib/b.go" } });
    fireEvent.click(screen.getByRole("button", { name: "Move" }));
    // No slice includes /lib, so there is nowhere to stage it.
    expect(await screen.findByRole("alert")).toHaveTextContent(/No slice you can edit/);
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

describe("copy and move to another slice or account", () => {
  const sourceEntry = {
    path: "/src/a.go",
    kind: "ENTRY_KIND_FILE",
    blobId: "blob1",
    contentHash: "hash1"
  };
  const acme = {
    ref: { account: "acme", slice: "acme" },
    definition: { includedPaths: ["/acme"] }
  };

  function renderWith(options: {
    canWriteAccount?: (account: string) => boolean;
    stageQuietly?: (edits: unknown[]) => Promise<string>;
  }) {
    const openChangeset = vi.fn();
    const stage = vi.fn();
    render(
      <CopyMoveProvider
        canModify={canModify}
        canWriteAccount={options.canWriteAccount ?? (() => true)}
        commitId="c1"
        openChangeset={openChangeset}
        slice={{ account: "nic", slice: "nic" }}
        stage={stage}
        stageQuietly={options.stageQuietly as never}
      >
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
    return { openChangeset, stage };
  }

  beforeEach(() => {
    api.resolvePath.mockImplementation(async ({ path }: { path: string }) => {
      if (path === "/src/a.go") {
        return { entry: sourceEntry };
      }
      throw notFound();
    });
    api.listSlices.mockResolvedValue({ slices: [acme] });
    api.createChangeset.mockResolvedValue({ id: "cs_copy", currentPatchsetId: "ps1" });
    api.updateChangeset.mockResolvedValue({ id: "ps2" });
  });

  it("copies into a changeset of the other account's slice", async () => {
    const { openChangeset, stage } = renderWith({});
    openDialog("Copy to…");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "/acme/docs/a.go" } });
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));

    fireEvent.click(await screen.findByRole("button", { name: "Open the copy" }));
    expect(openChangeset).toHaveBeenCalledWith("cs_copy");
    expect(stage).not.toHaveBeenCalled();
    expect(api.listSlices).toHaveBeenCalledWith(
      expect.objectContaining({ account: "acme" })
    );
    expect(api.createChangeset).toHaveBeenCalledWith(
      expect.objectContaining({
        authoringSlice: { account: "acme", slice: "acme" },
        baseCommitId: "c1"
      })
    );
    expect(api.updateChangeset).toHaveBeenCalledWith({
      baseCommitId: "c1",
      changesetId: "cs_copy",
      expectedCurrentPatchsetId: "ps1",
      fileEdits: [
        { op: "add", path: "/acme/docs/a.go", blobId: "blob1", contentHash: "hash1" }
      ]
    });
  });

  it("moves by copying there and staging the delete here", async () => {
    const stageQuietly = vi.fn().mockResolvedValue("cs_delete");
    const { openChangeset } = renderWith({ stageQuietly });
    openDialog("Move to…");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "/acme/docs/a.go" } });
    fireEvent.click(screen.getByRole("button", { name: "Move" }));

    const deleteLink = await screen.findByRole("button", { name: "Open the delete" });
    expect(screen.getByRole("button", { name: "Open the copy" })).toBeInTheDocument();
    fireEvent.click(deleteLink);
    expect(stageQuietly).toHaveBeenCalledWith([{ kind: "delete", path: "/src/a.go" }]);
    expect(openChangeset).toHaveBeenCalledWith("cs_delete");
  });

  it("says so when the delete could not be staged after the copy was", async () => {
    const stageQuietly = vi.fn().mockRejectedValue(new Error("save failed"));
    renderWith({ stageQuietly });
    openDialog("Move to…");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "/acme/docs/a.go" } });
    fireEvent.click(screen.getByRole("button", { name: "Move" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/save failed/);
    expect(screen.getByRole("button", { name: "Open the copy" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open the delete" })).not.toBeInTheDocument();
  });

  it("refuses an account the viewer can't write to, creating nothing", async () => {
    renderWith({ canWriteAccount: () => false });
    openDialog("Copy to…");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "/acme/docs/a.go" } });
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/can't change files in acme/);
    expect(api.createChangeset).not.toHaveBeenCalled();
  });

  it("does not offer a path only the slice's root names", async () => {
    renderWith({});
    openDialog("Copy to…");
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "/acme" } });
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/No slice you can edit/);
    expect(api.createChangeset).not.toHaveBeenCalled();
  });
});
