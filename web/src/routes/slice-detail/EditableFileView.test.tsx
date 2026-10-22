import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render as baseRender, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { EditableFileView } from "./EditableFileView";

// Blame, once on, asks the API for it.
vi.mock("../../api/useApi", () => ({
  useApi: () => ({ blameFile: vi.fn().mockResolvedValue({ commits: [], lineCount: 1, ranges: [] }) })
}));

const render = (ui: ReactElement) =>
  baseRender(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>);

afterEach(cleanup);

describe("EditableFileView", () => {
  it("uses the binary image payload instead of decoded text", () => {
    render(
      <EditableFileView
        commitId="commit_12345678"
        fileContent="replacement characters should not render"
        fileData="iVBORw0KGgo="
        includedPaths={["/public"]}
        onOpenHistory={vi.fn()}
        pendingEdits={[]}
        selectedPath="/public/apple-touch-icon.png"
      />
    );

    expect(
      screen.getByRole("img", { name: "Preview of apple-touch-icon.png" })
    ).toHaveAttribute("src", "data:image/png;base64,iVBORw0KGgo=");
    expect(
      screen.queryByText("replacement characters should not render")
    ).not.toBeInTheDocument();
  });

  it("does not offer text editing for an image file", () => {
    render(
      <EditableFileView
        commitId="commit_12345678"
        fileContent=""
        fileData="iVBORw0KGgo="
        includedPaths={["/public"]}
        onOpenHistory={vi.fn()}
        onStageEdit={vi.fn()}
        pendingEdits={[]}
        selectedPath="/public/apple-touch-icon.png"
      />
    );

    fireEvent.click(screen.getByRole("button", { name: "File actions" }));

    expect(
      screen.queryByRole("menuitem", { name: "Edit" })
    ).not.toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Rename" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Delete" })).toBeInTheDocument();
  });

  describe("the file's menu", () => {
    const slice = { account: "acme", slice: "app" };
    const text = (props: Partial<Parameters<typeof EditableFileView>[0]> = {}) => (
      <EditableFileView
        commitId="commit_12345678"
        fileContent="package app\n"
        fileData=""
        includedPaths={["/acme/app"]}
        onOpenHistory={vi.fn()}
        onSelectPath={vi.fn()}
        pendingEdits={[]}
        selectedPath="/acme/app/main.go"
        sliceRef={slice}
        {...props}
      />
    );

    it("has Blame, as a check that shows whether it is on, for someone who cannot edit", () => {
      const onChange = vi.fn();
      const { rerender } = render(text({ blame: { at: "", on: false, onChange } }));
      fireEvent.click(screen.getByRole("button", { name: "File actions" }));
      const item = screen.getByRole("menuitemcheckbox", { name: "Blame" });
      expect(item).toHaveAttribute("aria-checked", "false");
      expect(screen.queryByRole("menuitem", { name: "Edit" })).not.toBeInTheDocument();

      fireEvent.click(item);
      expect(onChange).toHaveBeenCalledWith(true, "");

      // When on, it reads as on, and choosing it again turns it off.
      rerender(
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          {text({ blame: { at: "abc", on: true, onChange } })}
        </QueryClientProvider>
      );
      fireEvent.click(screen.getByRole("button", { name: "File actions" }));
      expect(screen.getByRole("menuitemcheckbox", { name: "Blame" })).toHaveAttribute("aria-checked", "true");
      fireEvent.click(screen.getByRole("menuitemcheckbox", { name: "Blame" }));
      expect(onChange).toHaveBeenLastCalledWith(false, "");
    });

    it("keeps History as a button on a wider screen and an item for phones only", () => {
      render(text({ blame: { at: "", on: false, onChange: vi.fn() } }));
      expect(screen.getByRole("button", { name: "History" })).toHaveClass("max-sm:hidden");
      fireEvent.click(screen.getByRole("button", { name: "File actions" }));
      expect(screen.getByRole("menuitem", { name: "History" })).toHaveClass("sm:hidden");
    });

    it("keeps Blame with the editing actions for someone who can edit", () => {
      render(text({ blame: { at: "", on: false, onChange: vi.fn() }, onStageEdit: vi.fn() }));
      fireEvent.click(screen.getByRole("button", { name: "File actions" }));
      for (const name of ["Edit", "Rename", "Delete"]) {
        expect(screen.getByRole("menuitem", { name })).toBeInTheDocument();
      }
      expect(screen.getByRole("menuitemcheckbox", { name: "Blame" })).toBeInTheDocument();
    });

    it("offers no Blame for an image, or for a file seen outside a slice", () => {
      render(text({ blame: { at: "", on: false, onChange: vi.fn() }, selectedPath: "/acme/app/logo.png", fileData: "iVBORw0KGgo=" }));
      fireEvent.click(screen.getByRole("button", { name: "File actions" }));
      expect(screen.queryByRole("menuitemcheckbox", { name: "Blame" })).not.toBeInTheDocument();
      cleanup();
      render(text({ blame: { at: "", on: false, onChange: vi.fn() }, sliceRef: undefined }));
      fireEvent.click(screen.getByRole("button", { name: "File actions" }));
      expect(screen.queryByRole("menuitemcheckbox", { name: "Blame" })).not.toBeInTheDocument();
    });
  });
});
