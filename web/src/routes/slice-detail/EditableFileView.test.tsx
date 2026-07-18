import "@testing-library/jest-dom/vitest";

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { EditableFileView } from "./EditableFileView";

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
});
