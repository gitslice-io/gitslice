import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { CopyMoveProvider } from "./CopyMove";
import { DirectoryHeader } from "./DirectoryHeader";
import { EditableFileView } from "./EditableFileView";
import { SliceDirectoryTable } from "./SliceDirectoryTable";

const api = vi.hoisted(() => ({
  copyPath: vi.fn(),
  listSlices: vi.fn(),
  movePath: vi.fn(),
  resolvePath: vi.fn()
}));

vi.mock("../../api/useApi", () => ({ useApi: () => api }));

const included = ["/src"];
const canModify = (path: string) => path.startsWith("/src/");
const home = { account: "nic", slice: "nic" };

function notFound() {
  return new Error("path not found");
}

function fileView() {
  return (
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
}

function renderFile(options: { canWriteAccount?: (account: string) => boolean } = {}) {
  const onLanded = vi.fn();
  render(
    <CopyMoveProvider
      canModify={canModify}
      canWriteAccount={options.canWriteAccount ?? (() => true)}
      commitId="c1"
      onLanded={onLanded}
      slice={home}
    >
      {fileView()}
    </CopyMoveProvider>
  );
  return onLanded;
}

function openDialog(item: string) {
  fireEvent.click(screen.getByRole("button", { name: "File actions" }));
  fireEvent.click(screen.getByRole("menuitem", { name: item }));
}

function submit(value: string, button: string) {
  fireEvent.change(screen.getByRole("textbox"), { target: { value } });
  fireEvent.click(screen.getByRole("button", { name: button }));
}

function landed(id: string) {
  return { changeset: { id }, commitId: `commit_${id}` };
}

beforeEach(() => {
  for (const fn of Object.values(api)) {
    fn.mockReset();
  }
  api.resolvePath.mockRejectedValue(notFound());
  api.copyPath.mockResolvedValue(landed("cs_copy"));
  api.movePath.mockResolvedValue(landed("cs_move"));
});
afterEach(cleanup);

describe("copy and move menus", () => {
  it("are not offered without the provider", () => {
    render(fileView());
    fireEvent.click(screen.getByRole("button", { name: "File actions" }));
    expect(screen.queryByRole("menuitem", { name: "Copy to…" })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Move to…" })).not.toBeInTheDocument();
  });

  it("disable moving a top-level slice folder but still allow copying it", () => {
    render(
      <CopyMoveProvider canModify={canModify} commitId="c1" onLanded={vi.fn()} slice={home}>
        <DirectoryHeader commitId="c1" includedPaths={included} onStageEdit={vi.fn()} selectedPath="/src" />
      </CopyMoveProvider>
    );
    fireEvent.click(screen.getByRole("button", { name: "Directory actions" }));
    expect(screen.getByRole("menuitem", { name: "Copy to…" })).toBeEnabled();
    expect(screen.getByRole("menuitem", { name: "Move to…" })).toBeDisabled();
  });

  it("appear on directory rows", () => {
    render(
      <CopyMoveProvider canModify={canModify} commitId="c1" onLanded={vi.fn()} slice={home}>
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

describe("copy and move in this slice", () => {
  it("moves at once and opens the destination", async () => {
    const onLanded = renderFile();
    openDialog("Move to…");
    submit("b.go", "Move");

    await waitFor(() =>
      expect(onLanded).toHaveBeenCalledWith({
        changesetId: "cs_move",
        commitId: "commit_cs_move",
        path: "/src/b.go",
        slice: home
      })
    );
    expect(api.movePath).toHaveBeenCalledWith({
      slice: home,
      sourcePath: "/src/a.go",
      destinationPath: "/src/b.go"
    });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("copies a whole folder with one request, whatever its size", async () => {
    const onLanded = vi.fn();
    render(
      <CopyMoveProvider canModify={canModify} commitId="c1" onLanded={onLanded} slice={home}>
        <DirectoryHeader commitId="c1" includedPaths={included} onStageEdit={vi.fn()} selectedPath="/src/pkg" />
      </CopyMoveProvider>
    );
    fireEvent.click(screen.getByRole("button", { name: "Directory actions" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Copy to…" }));
    submit("pkg2", "Copy");

    await waitFor(() => expect(onLanded).toHaveBeenCalled());
    expect(api.copyPath).toHaveBeenCalledTimes(1);
    expect(api.copyPath).toHaveBeenCalledWith({
      slice: home,
      sourcePath: "/src/pkg",
      destinationPath: "/src/pkg2"
    });
  });

  it("puts the item inside an existing folder", async () => {
    api.resolvePath.mockImplementation(async ({ path }: { path: string }) => {
      if (path === "/src/pkg") {
        return { entry: { path, kind: "ENTRY_KIND_DIRECTORY" } };
      }
      throw notFound();
    });
    renderFile();
    openDialog("Move to…");
    submit("pkg", "Move");
    await waitFor(() =>
      expect(api.movePath).toHaveBeenCalledWith(
        expect.objectContaining({ destinationPath: "/src/pkg/a.go" })
      )
    );
  });

  it("refuses to overwrite an existing file", async () => {
    api.resolvePath.mockImplementation(async ({ path }: { path: string }) => ({
      entry: { path, kind: "ENTRY_KIND_FILE" }
    }));
    const onLanded = renderFile();
    openDialog("Copy to…");
    submit("b.go", "Copy");
    expect(await screen.findByRole("alert")).toHaveTextContent(/already exists/);
    expect(api.copyPath).not.toHaveBeenCalled();
    expect(onLanded).not.toHaveBeenCalled();
  });

  it("shows the server's error and stays open", async () => {
    api.movePath.mockRejectedValue(new Error("/src/b.go already exists"));
    const onLanded = renderFile();
    openDialog("Move to…");
    submit("b.go", "Move");
    expect(await screen.findByRole("alert")).toHaveTextContent("/src/b.go already exists");
    expect(onLanded).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});

describe("copy to another slice or account", () => {
  const acme = {
    ref: { account: "acme", slice: "acme" },
    definition: { includedPaths: ["/acme"] }
  };

  beforeEach(() => {
    api.listSlices.mockResolvedValue({ slices: [acme] });
  });

  it("lands in the slice that covers the destination, reading through this one", async () => {
    const onLanded = renderFile();
    openDialog("Copy to…");
    submit("/acme/docs/a.go", "Copy");

    await waitFor(() =>
      expect(onLanded).toHaveBeenCalledWith({
        changesetId: "cs_copy",
        commitId: "commit_cs_copy",
        path: "/acme/docs/a.go",
        slice: { account: "acme", slice: "acme" }
      })
    );
    expect(api.listSlices).toHaveBeenCalledWith(expect.objectContaining({ account: "acme" }));
    expect(api.copyPath).toHaveBeenCalledWith({
      slice: { account: "acme", slice: "acme" },
      sourcePath: "/src/a.go",
      destinationPath: "/acme/docs/a.go",
      sourceSlice: home
    });
  });

  it("refuses a move out of this slice, without any request", async () => {
    const onLanded = renderFile();
    openDialog("Move to…");
    submit("/acme/docs/a.go", "Move");

    expect(await screen.findByRole("alert")).toHaveTextContent(/move can't leave this slice/i);
    expect(api.listSlices).not.toHaveBeenCalled();
    expect(api.movePath).not.toHaveBeenCalled();
    expect(onLanded).not.toHaveBeenCalled();
  });

  it("refuses an account the viewer can't write to", async () => {
    renderFile({ canWriteAccount: () => false });
    openDialog("Copy to…");
    submit("/acme/docs/a.go", "Copy");

    expect(await screen.findByRole("alert")).toHaveTextContent(/can't change files in acme/);
    expect(api.copyPath).not.toHaveBeenCalled();
  });

  it("refuses a path no slice includes below its top-level folder", async () => {
    renderFile();
    openDialog("Copy to…");
    submit("/acme", "Copy");

    expect(await screen.findByRole("alert")).toHaveTextContent(/No slice you can edit/);
    expect(api.copyPath).not.toHaveBeenCalled();
  });
});
